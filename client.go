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
		tags := map[string]string{
			"task": data.Name,
		}

		metrics.PushIfNotDone(ctx, state.Samples, metrics.Sample{
			Time:   eventTime,
			Metric: parent.metrics.ChildTasks,
			Tags:   metrics.IntoSampleTags(&tags),
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
		tags := map[string]string{
			"task": task.taskName,
		}

		metrics.PushIfNotDone(ctx, state.Samples, metrics.Sample{
			Time:   eventTime,
			Metric: task.metrics.Tasks,
			Tags:   metrics.IntoSampleTags(&tags),
			Value:  1,
		})

		queueTimeNano := (event.Timestamp - task.sentAt) * 1000
		metrics.PushIfNotDone(ctx, state.Samples, metrics.Sample{
			Time:   eventTime,
			Metric: task.metrics.TaskQueueTime,
			Tags:   metrics.IntoSampleTags(&tags),
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
		tags := map[string]string{
			"task": task.taskName,
		}

		taskSucceededVal := 0.
		if succeeded {
			taskSucceededVal = 1
		}

		metrics.PushIfNotDone(ctx, state.Samples, metrics.Sample{
			Time:   eventTime,
			Metric: task.metrics.TasksSucceeded,
			Tags:   metrics.IntoSampleTags(&tags),
			Value:  taskSucceededVal,
		})

		runtimeNanoSec := (event.Timestamp - task.startedAt) * 1000
		metrics.PushIfNotDone(ctx, state.Samples, metrics.Sample{
			Time:   eventTime,
			Metric: task.metrics.TaskRuntime,
			Tags:   metrics.IntoSampleTags(&tags),
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
		tags := map[string]string{
			"task": task.taskName,
		}

		metrics.PushIfNotDone(ctx, state.Samples, metrics.Sample{
			Time:   eventTime,
			Metric: task.metrics.TasksRetried,
			Tags:   metrics.IntoSampleTags(&tags),
			Value:  1,
		})

		runtimeNanoSec := (event.Timestamp - task.startedAt) * 1000
		metrics.PushIfNotDone(ctx, state.Samples, metrics.Sample{
			Time:   eventTime,
			Metric: task.metrics.TaskRuntime,
			Tags:   metrics.IntoSampleTags(&tags),
			Value:  runtimeNanoSec,
		})

		task.sentAt = event.Timestamp
	}
}

type TaskRunArgs struct {
	TaskName   string
	Args       []any
	Kwargs     map[string]any
	Serializer string
}

func (client *Client) RunTask(
	params *TaskRunArgs,
	vu modules.VU,
	metrics *celeryMetrics) error {

	if params.TaskName == "" {
		return errors.New("taskName is required")
	}

	serializerType := "json"
	if params.Serializer != "" {
		serializerType = params.Serializer
	}

	codec, ok := codecs[serializerType]
	if !ok {
		return fmt.Errorf("unknown serializer %s", serializerType)
	}

	if params.Args == nil {
		params.Args = make([]any, 0)
	}

	if params.Kwargs == nil {
		params.Kwargs = make(map[string]any)
	}

	body := []any{params.Args, params.Kwargs, getCeleryOptions()}
	serializedBody, err := codec.Serializer(body)
	if err != nil {
		return err
	}

	taskId := uuid.New().String()
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
			"task":          params.TaskName,
		},
		Body: serializedBody,
	}

	waitGroup := sync.WaitGroup{}
	waitGroup.Add(1)
	client.runningTasks.Store(taskId, &celeryTask{
		vu:      vu,
		metrics: metrics,

		taskId:   taskId,
		taskName: params.TaskName,

		sentAt: timeToFloat(time.Now()),

		waitGroup: &waitGroup,
	})

	err = client.publishChannel.Publish(
		"",
		client.queueName,
		false,
		false,
		publishing,
	)

	if err != nil {
		client.runningTasks.Delete(taskId)
		return err
	}

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
