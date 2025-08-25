package sqs

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/ThreeDotsLabs/watermill"
	"github.com/ThreeDotsLabs/watermill/message"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/aws/smithy-go"
)

type Subscriber struct {
	config SubscriberConfig
	logger watermill.LoggerAdapter
	sqs    *sqs.Client

	closing       chan struct{}
	subscribersWg sync.WaitGroup

	closed     bool
	closedLock sync.Mutex
}

func NewSubscriber(config SubscriberConfig, logger watermill.LoggerAdapter) (*Subscriber, error) {
	config.SetDefaults()
	if err := config.Validate(); err != nil {
		return nil, err
	}

	if logger == nil {
		logger = watermill.NopLogger{}
	}

	logger = logger.With(watermill.LogFields{
		"subscriber_uuid": watermill.NewShortUUID(),
	})

	return &Subscriber{
		config:  config,
		logger:  logger,
		sqs:     sqs.NewFromConfig(config.AWSConfig, config.OptFns...),
		closing: make(chan struct{}),
	}, nil
}

func (s *Subscriber) Subscribe(ctx context.Context, topic string) (<-chan *message.Message, error) {
	if s.closed {
		return nil, errors.New("subscriber closed")
	}

	s.logger.With(watermill.LogFields{"topic": topic}).Debug("Getting queue", nil)

	resolveQueueParams := ResolveQueueUrlParams{
		Topic:     topic,
		SqsClient: s.sqs,
		Logger:    s.logger,
	}

	resolvedQueue, err := s.config.QueueUrlResolver.ResolveQueueUrl(ctx, resolveQueueParams)
	if err != nil {
		return nil, err
	}
	// if we already know we are creating the queue - if not we'll create it later
	if resolvedQueue.Exists != nil && !*resolvedQueue.Exists {
		if s.config.DoNotCreateQueueIfNotExists {
			return nil, fmt.Errorf("queue for topic '%s' doesn't exists", topic)
		}

		input, err := s.config.GenerateCreateQueueInput(ctx, resolvedQueue.QueueName, s.config.QueueConfigAttributes)
		if err != nil {
			return nil, fmt.Errorf("cannot generate input for queue %s: %w", topic, err)
		}

		_, err = createQueue(ctx, s.sqs, input)
		if err != nil {
			return nil, fmt.Errorf("cannot create queue %s: %w", topic, err)
		}

		resolvedQueue, err = s.config.QueueUrlResolver.ResolveQueueUrl(ctx, resolveQueueParams)
		if err != nil {
			return nil, err
		}
	}

	receiveInput, err := s.config.GenerateReceiveMessageInput(ctx, *resolvedQueue.QueueURL)
	if err != nil {
		return nil, fmt.Errorf("cannot generate input for topic %s: %w", topic, err)
	}

	s.logger.With(watermill.LogFields{"queue": *resolvedQueue.QueueURL}).Info("Subscribing to queue", nil)

	ctx, cancel := context.WithCancel(ctx)
	s.subscribersWg.Add(1)
	output := make(chan *message.Message)

	go func() {
		s.receive(ctx, *resolvedQueue.QueueURL, output, receiveInput)
		close(output)
		cancel()
	}()

	return output, nil
}

func (s *Subscriber) receive(ctx context.Context, queueURL QueueURL, output chan *message.Message, input *sqs.ReceiveMessageInput) {
	defer s.subscribersWg.Done()
	ctx, cancelCtx := context.WithCancel(ctx)
	defer cancelCtx()
	logFields := watermill.LogFields{
		"provider": "aws",
		"queue":    queueURL,
	}

	go func() {
		<-s.closing
		cancelCtx()
	}()

	var sleepTime time.Duration = 0
	for {
		select {
		case <-s.closing:
			s.logger.Debug("Discarding queued message, subscriber closing", logFields)
			return

		case <-ctx.Done():
			s.logger.Debug("Stopping consume, context canceled", logFields)
			return

		case <-time.After(sleepTime):
			// Wait if needed
		}

		result, err := s.sqs.ReceiveMessage(ctx, input)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				sleepTime = NoSleep
				continue
			} else {
				s.logger.Error("Cannot connect receive messages", err, logFields)
				sleepTime = s.config.ReconnectRetrySleep
				continue
			}
		}

		sleepTime = NoSleep
		if result == nil || len(result.Messages) == 0 {
			s.logger.Trace("No messages", logFields)
			continue
		}
		s.consumeMessages(ctx, result.Messages, queueURL, output, logFields)
	}
}

func (s *Subscriber) consumeMessages(
	ctx context.Context,
	messages []types.Message,
	queueURL QueueURL,
	output chan *message.Message,
	logFields watermill.LogFields,
) {
	for _, sqsMsg := range messages {
		processed := s.processMessage(ctx, logFields, sqsMsg, output, queueURL)

		if !processed {
			return
		}
	}
}

func (s *Subscriber) processMessage(
	ctx context.Context,
	logFields watermill.LogFields,
	sqsMsg types.Message,
	output chan *message.Message,
	queueURL QueueURL,
) bool {
	logger := s.logger.With(logFields)
	logger.Trace("processMessage", nil)

	ctx, cancelCtx := context.WithCancel(ctx)
	defer cancelCtx()

	msg, err := s.config.Unmarshaler.Unmarshal(&sqsMsg)
	if err != nil {
		logger.Error("Cannot unmarshal message", err, logFields)
		return false
	}
	msg.SetContext(ctx)

	logger = s.logger.With(logFields).With(watermill.LogFields{
		"message_uuid": msg.UUID,
	})

	output <- msg

	select {
	case <-msg.Acked():
		err := s.deleteMessage(ctx, queueURL, sqsMsg.ReceiptHandle, logFields)
		if errors.Is(err, context.Canceled) {
			return false
		}
		if err != nil {
			logger.Error("Failed to delete message", err, logFields)
			return false
		}
	case <-msg.Nacked():
		// Do not delete message, it will be redelivered
		logger.Debug("Nacking message", logFields)
		return false // we don't want to process next messages to preserve order for FIFO
	case <-s.closing:
		logger.Debug("Closing, message discarded before ack", logFields)
		return false
	case <-ctx.Done():
		logger.Debug("Closing, ctx cancelled before ack", logFields)
		return false
	}

	return true
}

func (s *Subscriber) deleteMessage(ctx context.Context, queueURL QueueURL, receiptHandle *string, logFields watermill.LogFields) error {
	input, err := s.config.GenerateDeleteMessageInput(ctx, queueURL, receiptHandle)
	if err != nil {
		return fmt.Errorf("cannot generate input for delete message: %w", err)
	}

	// we are using ctx that may be canceled when subscriber is closed
	//
	// it may lead to re-delivery when message is processed and in the meantime
	// subscriber is closed - but we don't know if context cancellation didn't cancel
	// some SQL transactions or whatever - so someone may lose data
	//
	// in other words, we prefer re-delivery (as at least once delivery is a thing anyway)
	_, err = s.sqs.DeleteMessage(ctx, input)
	if err != nil {
		var oe *smithy.GenericAPIError
		if errors.As(err, &oe) {
			// todo(roblaszczak): it would be nice to replace it with a specific error type
			// but I wasn't able to reproduce it
			if oe.Message == "The specified queue does not contain the message specified." {
				s.logger.Debug("Message was already deleted or is not in queue", logFields)
				return nil
			}
		}
		return fmt.Errorf("cannot ack (delete) message: %w", err)
	}

	return nil
}

func (s *Subscriber) Close() error {
	s.closedLock.Lock()
	defer s.closedLock.Unlock()

	if s.closed {
		return nil
	}

	s.closed = true

	close(s.closing)
	s.subscribersWg.Wait()

	return nil
}

func (s *Subscriber) SubscribeInitialize(topic string) error {
	return s.SubscribeInitializeWithContext(context.Background(), topic)
}

func (s *Subscriber) SubscribeInitializeWithContext(ctx context.Context, topic string) error {
	logger := s.logger.With(watermill.LogFields{
		"topic": topic,
	})
	logger.Debug("Initializing SQS subscription", nil)

	resolvedQueue, err := s.config.QueueUrlResolver.ResolveQueueUrl(ctx, ResolveQueueUrlParams{
		Topic:     topic,
		SqsClient: s.sqs,
		Logger:    s.logger,
	})

	logger.Debug("Topic resolving done", watermill.LogFields{
		"resolved_queue": resolvedQueue,
		"err":            err,
	})
	if err != nil {
		return err
	}
	if resolvedQueue.Exists != nil && *resolvedQueue.Exists {
		return nil
	}

	if s.config.DoNotCreateQueueIfNotExists {
		return fmt.Errorf("queue for topic '%s' doesn't exists", topic)
	}

	input, err := s.config.GenerateCreateQueueInput(ctx, resolvedQueue.QueueName, s.config.QueueConfigAttributes)
	if err != nil {
		return fmt.Errorf("cannot generate input for queue %s: %w", topic, err)
	}

	logger.Debug("Creating queue", watermill.LogFields{
		"queue_name": *input.QueueName,
	})

	_, err = createQueue(ctx, s.sqs, input)
	if err != nil {
		return fmt.Errorf("cannot create queue %s: %w", topic, err)
	}

	return nil
}

func (s *Subscriber) GetQueueUrl(ctx context.Context, topic string) (*QueueURL, error) {
	resolvedQueue, err := s.config.QueueUrlResolver.ResolveQueueUrl(ctx, ResolveQueueUrlParams{
		Topic:     topic,
		SqsClient: s.sqs,
		Logger:    s.logger,
	})
	if err != nil {
		return nil, fmt.Errorf("cannot generate input for queue %s: %w", topic, err)
	}
	if resolvedQueue.Exists != nil && !*resolvedQueue.Exists {
		return nil, fmt.Errorf("queue for topic '%s' doesn't exist", topic)
	}

	return resolvedQueue.QueueURL, nil
}

func (s *Subscriber) GetQueueArn(ctx context.Context, url *QueueURL) (*QueueArn, error) {
	return getARNUrl(ctx, s.sqs, url)
}

const NoSleep time.Duration = -1
