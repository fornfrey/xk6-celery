package celery

import (
	"encoding/json"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/rabbitmq/amqp091-go"
)

type Client struct {
	brokerUrl string

	publishChannel *amqp091.Channel

	runningTasks sync.Map
}

func NewClient(brokerUrl string) (*Client, error) {

	client := &Client{
		brokerUrl: brokerUrl,
	}

	publishConnection, err := client.dial()
	if err != nil {
		return nil, err
	}

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

func (celery *Client) dial() (*amqp091.Connection, error) {
	connection, err := amqp091.Dial(celery.brokerUrl)
	return connection, err
}

func (client *Client) startEventsConsumer(brokerUrl string) error {
	consumerConnection, err := client.dial()
	if err != nil {
		return err
	}

	consumerChannel, err := consumerConnection.Channel()
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
	err := json.Unmarshal(msg.Body, &eventsRaw)
	if err != nil {
		return
	}

	for _, eventRaw := range eventsRaw {

		type eventType struct {
			Value string `json:"type"`
		}

		et := eventType{}
		err := json.Unmarshal(eventRaw, &et)
		if err != nil {
			continue
		}

		switch et.Value {
		case "task-succeeded":
			client.handleTaskSucceeded(eventRaw)
		case "task-failed":
			client.handleTaskFailed(eventRaw)
		default:
			continue
		}
	}
}

func (client *Client) handleTaskSucceeded(eventRaw json.RawMessage) {
	event := taskSucceeded{}
	if err := json.Unmarshal(eventRaw, &event); err != nil {
		panic(err)
	}
	client.completeTask(event.TaskId)
}

func (client *Client) handleTaskFailed(eventRaw json.RawMessage) {
	event := taskFailed{}
	if err := json.Unmarshal(eventRaw, &event); err != nil {
		panic(err)
	}
	client.completeTask(event.TaskId)
}

func (client *Client) completeTask(taskId string) bool {
	entry, ok := client.runningTasks.LoadAndDelete(taskId)

	if ok {
		callback := entry.(func())
		callback()
	}

	return ok
}

func (client *Client) notifyWhenCompleted(taskId string, callback func()) {
	client.runningTasks.Store(taskId, callback)
}

func (client *Client) CallTask(task string, args []interface{}) error {

	taskId := uuid.New().String()
	celeryMessageBody, err := getCeleryMessageBody(args, make(map[string]interface{}))
	if err != nil {
		return err
	}

	publishing := amqp091.Publishing{
		CorrelationId:   uuid.New().String(),
		Priority:        0,
		DeliveryMode:    amqp091.Persistent,
		ContentEncoding: "utf-8",
		ContentType:     "application/json",
		Headers: amqp091.Table{
			"id":            taskId,
			"ignore_result": true,
			"root_id":       taskId,
			"task":          task,
		},
		Body: celeryMessageBody,
	}

	err = client.publishChannel.Publish(
		"",
		"k6-api-events",
		false,
		false,
		publishing,
	)

	if err != nil {
		return err
	}

	completed := make(chan interface{})
	client.notifyWhenCompleted(taskId, func() {
		completed <- nil
	})

	select {
	case <-completed:
	case <-time.After(time.Second * 60):
		client.runningTasks.Delete(taskId)
	}

	return nil
}

func getCeleryMessageBody(args []interface{}, kwargs map[string]interface{}) ([]byte, error) {
	body := []interface{}{
		args,
		kwargs,
		map[string]interface{}{"callbacks": nil, "errbacks": nil, "chain": nil, "chord": nil},
	}
	return json.Marshal(body)
}
