package infra

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/fystack/multichain-indexer/pkg/common/logger"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

const (
	TransferEventTopicQueue = "transfer.event.dispatch"
	UTXOEventTopicQueue     = "transfer.utxo.dispatch"
)

var (
	ErrPermament = errors.New("permanent messaging error")
	MaxMsgSize   = 10 * 1024 // 10KB
)

type MessageQueue interface {
	Enqueue(topic string, message []byte, options *EnqueueOptions) error
	// handler shouldn't be a blocking call as it would trigger redivery of the message
	// if certain period of time has passed without ack.
	Dequeue(topic string, handler func(message []byte) error) error
	Close()
}

type EnqueueOptions struct {
	IdempotententKey string
}

type msgQueue struct {
	consumerName    string
	js              jetstream.JetStream
	consumer        jetstream.Consumer
	consumerContext jetstream.ConsumeContext
	useBackoffRetry bool
}

type NATsMessageQueueManager struct {
	queueName string
	js        jetstream.JetStream
}

func NewNATsMessageQueueManager(queueName string, subjectWildCards []string, nc *nats.Conn) *NATsMessageQueueManager {
	js, err := jetstream.New(nc)
	if err != nil {
		logger.Fatal("Error creating JetStream context: ", err)
	}

	ctx := context.Background()
	stream, err := js.Stream(ctx, queueName)
	if err != nil {
		logger.Warn("Stream not found, creating new stream", "stream", queueName)
	}
	if stream != nil {
		info, _ := stream.Info(ctx)
		logger.Info("Stream found", "name", info.Config.Name, "subjects", info.Config.Subjects, "state", info.State.Msgs)
	}

	_, err = js.CreateOrUpdateStream(context.Background(), jetstream.StreamConfig{
		Name:        queueName,
		Description: "Stream for " + queueName,
		Subjects:    subjectWildCards,
		MaxBytes:    int64(MaxMsgSize),
		Storage:     jetstream.FileStorage,
		Retention:   jetstream.WorkQueuePolicy,
		MaxAge:      2 * 24 * time.Hour, // 2 days
	})
	if err != nil {
		logger.Fatal("Error creating JetStream stream: ", err)
	}
	logger.Info("Creating apex NATs Jetstream context successfully!")

	return &NATsMessageQueueManager{
		queueName: queueName,
		js:        js,
	}
}

func (m *NATsMessageQueueManager) NewMessageQueue(consumerName string) MessageQueue {
	mq := &msgQueue{
		consumerName: consumerName,
		js:           m.js,
	}
	consumerWildCard := fmt.Sprintf("%s.%s.*", m.queueName, consumerName)
	cfg := jetstream.ConsumerConfig{
		Name:          consumerName,
		Durable:       consumerName,
		MaxAckPending: 4,
		FilterSubjects: []string{
			consumerWildCard,
		},
		MaxDeliver: 3,
	}
	logger.Info("Creating consumer for subject", "name", cfg.Name, "durable", cfg.Durable, "filterSubjects", cfg.FilterSubjects)
	consumer, err := m.js.CreateOrUpdateConsumer(context.Background(), m.queueName, cfg)
	if err != nil {
		logger.Fatal("Error creating JetStream consumer: ", err)
	}

	mq.consumer = consumer
	return mq
}
func (m *NATsMessageQueueManager) NewMessageQueueWithBackoff(consumerName string, backoffIntervals []time.Duration, maxDeliver int) MessageQueue {
	mq := &msgQueue{
		consumerName: consumerName,
		js:           m.js,
	}

	consumerWildCard := fmt.Sprintf("%s.%s.*", m.queueName, consumerName)
	logger.Info("Creating consumer with custom backoff for subject", "consumerName", consumerName, "consumerWildCard", consumerWildCard)

	cfg := jetstream.ConsumerConfig{
		Name:          consumerName,
		Durable:       consumerName,
		MaxAckPending: 4, // Adjust based on the number of concurrent handlers
		FilterSubjects: []string{
			consumerWildCard,
		},
		MaxDeliver: maxDeliver,       // Maximum delivery attempts before sending to DLQ
		BackOff:    backoffIntervals, // Custom backoff intervals
	}

	logger.Info("Creating consumer with custom backoff for subject", "config", cfg)
	consumer, err := m.js.CreateOrUpdateConsumer(context.Background(), m.queueName, cfg)
	if err != nil {
		logger.Fatal("Error creating JetStream consumer with custom backoff: ", err)
	}

	mq.consumer = consumer
	mq.useBackoffRetry = true
	return mq
}

func (mq *msgQueue) Enqueue(topic string, message []byte, options *EnqueueOptions) error {
	logger.Info("Enqueueing message", "topic", topic, "message size", len(message))
	header := nats.Header{}
	if options != nil {
		header.Add("Nats-Msg-Id", options.IdempotententKey)
	}

	_, err := mq.js.PublishMsg(context.Background(), &nats.Msg{
		Subject: topic,
		Data:    message,
		Header:  header,
	})

	if err != nil {
		return fmt.Errorf("error enqueueing message: %w", err)
	}
	return nil
}

func (mq *msgQueue) Dequeue(topic string, handler func(message []byte) error) error {
	logger.Info("Dequeuing message", "topic", topic)
	c, err := mq.consumer.Consume(func(msg jetstream.Msg) {
		meta, _ := msg.Metadata()
		logger.Info("Received message", "meta", meta)
		err := handler(msg.Data())
		if err != nil {
			if errors.Is(err, ErrPermament) {
				logger.Info("Permanent error on message", "meta", meta)
				msg.Term()
				return
			}

			logger.Error("error handling message: ", err)
			if !mq.useBackoffRetry {
				// msg.Nak() will retry immediately, so don't use it with backoff
				msg.Nak()
			}
			return
		}

		logger.Info("Message Acknowledged", "meta", meta)
		err = msg.Ack()
		if err != nil {
			logger.Error("Error acknowledging message: ", err)
		}
	})
	mq.consumerContext = c
	return err
}

func (mq *msgQueue) Close() {
	if mq.consumerContext != nil {
		mq.consumerContext.Stop()
	}
}

func (n *msgQueue) handleReconnect(nc *nats.Conn) {
	logger.Info("NATS: Reconnected to NATS")
}
