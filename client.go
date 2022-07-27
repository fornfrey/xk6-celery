package celery

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/rabbitmq/amqp091-go"
	"go.k6.io/k6/js/modules"
	"go.k6.io/k6/metrics"
)

type Client struct {
	consumeConnection *amqp091.Connection
	publishConnection *amqp091.Connection

	publishChannel *amqp091.Channel

	runningTasks sync.Map

	queueName string
}

type celeryTask struct {
	vu      modules.VU
	metrics *celeryMetrics

	taskId   string
	taskName string

	startedAt float64
	sentAt    float64

	waitGroup *sync.WaitGroup
}

type taskEvent struct {
	Type      string  `json:"type"`
	TaskId    string  `json:"uuid"`
	Timestamp float64 `json:"timestamp"`
}

func newClient(brokerUrl string, queueName string) (*Client, error) {

	client := &Client{
		queueName: queueName,
	}

	publishConnection, err := client.dial(brokerUrl)
	if err != nil {
		return nil, err
	}

	client.publishConnection = publishConnection

	publishChannel, err := publishConnection.Channel()
	if err != nil {
		return nil, err
	}

	client.publishChannel = publishChannel

	err = client.startEventsConsumer(brokerUrl)
	if err != nil {
		return nil, err
	}

	return client, nil
}

func (client *Client) dial(brokerUrl string) (*amqp091.Connection, error) {
	connection, err := amqp091.Dial(brokerUrl)
	return connection, err
}

func (client *Client) startEventsConsumer(brokerUrl string) error {
	consumeConnection, err := client.dial(brokerUrl)
	if err != nil {
		return err
	}

	client.consumeConnection = consumeConnection

	consumerChannel, err := consumeConnection.Channel()
	if err != nil {
		return err
	}

	eventsExchange := "celeryev"
	err = consumerChannel.ExchangeDeclare(eventsExchange, "topic", true, false, false, false, nil)
	if err != nil {
		return err
	}

	queue, err := consumerChannel.QueueDeclare("k6-task-events", false, true, false, false, nil)
	if err != nil {
		return err
	}

	err = consumerChannel.QueueBind(queue.Name, "task.#", "celeryev", false, nil)
	if err != nil {
		return err
	}

	delivery, err := consumerChannel.Consume(queue.Name, "k6", true, false, false, false, nil)
	if err != nil {
		return err
	}

	go func() {
		for msg := range delivery {
			client.receiveDelivery(&msg)
		}
	}()

	return nil
}

func (client *Client) receiveDelivery(msg *amqp091.Delivery) {

	var eventsRaw []json.RawMessage

	switch msg.RoutingKey {
	case "task.multi":
		err := json.Unmarshal(msg.Body, &eventsRaw)
		if err != nil {
			return
		}
	case "task.sent":
		eventsRaw = []json.RawMessage{msg.Body}
	default:
		panic(fmt.Errorf("unknown routing key %q", msg.RoutingKey))
	}

	for _, eventRaw := range eventsRaw {

		event := taskEvent{}
		err := json.Unmarshal(eventRaw, &event)
		if err != nil {
			continue
		}

		switch event.Type {
		case "task-sent":
			client.handleSent(event, eventRaw)
		case "task-started":
			client.handleStarted(event)
		case "task-succeeded":
			client.handleFinished(event, true)
		case "task-failed":
			client.handleFinished(event, false)
		case "task-retried":
			client.handleRetried(event)
		}
	}
}

func (client *Client) handleSent(event taskEvent, eventRaw json.RawMessage) {
	type taskSentData struct {
		Name     string `json:"name"`
		ParentId string `json:"parent_id"`
	}

	data := taskSentData{}
	json.Unmarshal(eventRaw, &data)

	entry, ok := client.runningTasks.Load(data.ParentId)
	if ok {
		parent := entry.(*celeryTask)

		client.runningTasks.Store(event.TaskId, &celeryTask{
			vu:      parent.vu,
			metrics: parent.metrics,

			taskId:   event.TaskId,
			taskName: data.Name,

			sentAt: event.Timestamp,

			waitGroup: parent.waitGroup,
		})

		parent.waitGroup.Add(1)

		ctx := parent.vu.Context()
		state := parent.vu.State()
		eventTime := floatToTime(event.Timestamp)
		tags := metrics.IntoSampleTags(&map[string]string{
			"task": data.Name,
		})

		metrics.PushIfNotDone(ctx, state.Samples, metrics.Sample{
			Time:   eventTime,
			Metric: parent.metrics.TasksTotal,
			Tags:   tags,
			Value:  1,
		})

		metrics.PushIfNotDone(ctx, state.Samples, metrics.Sample{
			Time:   eventTime,
			Metric: parent.metrics.ChildTasks,
			Tags:   tags,
			Value:  1,
		})
	}
}

func (client *Client) handleStarted(event taskEvent) {
	entry, ok := client.runningTasks.Load(event.TaskId)
	if ok {
		task := entry.(*celeryTask)

		ctx := task.vu.Context()
		state := task.vu.State()
		eventTime := floatToTime(event.Timestamp)
		tags := metrics.IntoSampleTags(&map[string]string{
			"task": task.taskName,
		})

		queueTimeNano := (event.Timestamp - task.sentAt) * 1000
		metrics.PushIfNotDone(ctx, state.Samples, metrics.Sample{
			Time:   eventTime,
			Metric: task.metrics.TaskQueueTime,
			Tags:   tags,
			Value:  queueTimeNano,
		})

		task.startedAt = event.Timestamp
	}
}

func (client *Client) handleFinished(event taskEvent, succeeded bool) {
	entry, ok := client.runningTasks.LoadAndDelete(event.TaskId)

	if ok {
		task := entry.(*celeryTask)

		ctx := task.vu.Context()
		state := task.vu.State()
		eventTime := floatToTime(event.Timestamp)
		tags := metrics.IntoSampleTags(&map[string]string{
			"task": task.taskName,
		})

		taskSucceededVal := 0.
		if succeeded {
			taskSucceededVal = 1
		}

		metrics.PushIfNotDone(ctx, state.Samples, metrics.Sample{
			Time:   eventTime,
			Metric: task.metrics.TasksSucceeded,
			Tags:   tags,
			Value:  taskSucceededVal,
		})

		runtimeNanoSec := (event.Timestamp - task.startedAt) * 1000
		metrics.PushIfNotDone(ctx, state.Samples, metrics.Sample{
			Time:   eventTime,
			Metric: task.metrics.TaskRuntime,
			Tags:   tags,
			Value:  runtimeNanoSec,
		})

		task.waitGroup.Done()
	}
}

func (client *Client) handleRetried(event taskEvent) {
	entry, ok := client.runningTasks.Load(event.TaskId)
	if ok {
		task := entry.(*celeryTask)

		ctx := task.vu.Context()
		state := task.vu.State()
		eventTime := floatToTime(event.Timestamp)
		tags := metrics.IntoSampleTags(&map[string]string{
			"task": task.taskName,
		})

		metrics.PushIfNotDone(ctx, state.Samples, metrics.Sample{
			Time:   eventTime,
			Metric: task.metrics.TasksRetried,
			Tags:   tags,
			Value:  1,
		})

		runtimeNanoSec := (event.Timestamp - task.startedAt) * 1000
		metrics.PushIfNotDone(ctx, state.Samples, metrics.Sample{
			Time:   eventTime,
			Metric: task.metrics.TaskRuntime,
			Tags:   tags,
			Value:  runtimeNanoSec,
		})

		task.sentAt = event.Timestamp
	}
}

func (client *Client) RunTask(sig *TaskSignature, vu modules.VU, vuMetrics *celeryMetrics) error {

	taskId := uuid.New().String()

	publishing, err := sig.Publishing(taskId)
	if err != nil {
		return err
	}

	waitGroup := sync.WaitGroup{}
	waitGroup.Add(1)

	now := time.Now()
	client.runningTasks.Store(taskId, &celeryTask{
		vu:      vu,
		metrics: vuMetrics,

		taskId:   taskId,
		taskName: sig.Name,

		sentAt: timeToFloat(now),

		waitGroup: &waitGroup,
	})

	err = client.publishChannel.Publish(
		"", // publish to the default exchange and directly to the queue
		client.queueName,
		false,
		false,
		publishing,
	)

	if err != nil {
		client.runningTasks.Delete(taskId)
		return err
	}

	tags := metrics.IntoSampleTags(&map[string]string{
		"task": sig.Name,
	})

	metrics.PushIfNotDone(vu.Context(), vu.State().Samples, metrics.Sample{
		Time:   now,
		Metric: vuMetrics.Tasks,
		Tags:   tags,
		Value:  1,
	})

	metrics.PushIfNotDone(vu.Context(), vu.State().Samples, metrics.Sample{
		Time:   now,
		Metric: vuMetrics.TasksTotal,
		Tags:   tags,
		Value:  1,
	})

	waitGroup.Wait()

	return nil
}

func getCeleryOptions() map[string]any {
	return map[string]any{"callbacks": nil, "errbacks": nil, "chain": nil, "chord": nil}
}

func floatToTime(t float64) time.Time {
	sec, dec := math.Modf(t)
	return time.Unix(int64(sec), int64(dec*(1e9)))
}

func timeToFloat(t time.Time) float64 {
	return float64(t.Unix()) + float64(t.Nanosecond())/1e9
}

type TaskSignature struct {
	Name       string
	Args       []any
	Kwargs     map[string]any
	Serializer string
}

func (sig *TaskSignature) Publishing(taskId string) (amqp091.Publishing, error) {
	if sig.Name == "" {
		return amqp091.Publishing{}, errors.New("'name' is required")
	}

	codec, err := sig.codec()
	if err != nil {
		return amqp091.Publishing{}, err
	}

	args := sig.Args
	if sig.Args == nil {
		args = make([]any, 0)
	}

	kwargs := sig.Kwargs
	if sig.Kwargs == nil {
		kwargs = make(map[string]any)
	}

	body := []any{args, kwargs, getCeleryOptions()}
	serializedBody, err := codec.Serializer(body)
	if err != nil {
		return amqp091.Publishing{}, err
	}

	publishing := amqp091.Publishing{
		CorrelationId:   uuid.New().String(),
		Priority:        0,
		DeliveryMode:    amqp091.Persistent,
		ContentEncoding: codec.Encoding,
		ContentType:     codec.ContentType,
		Headers: amqp091.Table{
			"id":            taskId,
			"ignore_result": true,
			"root_id":       taskId,
			"task":          sig.Name,
		},
		Body: serializedBody,
	}
	return publishing, nil
}

func (args *TaskSignature) codec() (*codec, error) {
	serializerType := "json" // json serializer is the default one
	if args.Serializer != "" {
		serializerType = args.Serializer
	}

	codec, ok := codecs[serializerType]
	if !ok {
		return nil, fmt.Errorf("unknown serializer %s", serializerType)
	}
	return codec, nil
}
