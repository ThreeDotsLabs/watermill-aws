package sqs

import (
	"context"
	"errors"
	"fmt"

	"github.com/ThreeDotsLabs/watermill"
	"github.com/ThreeDotsLabs/watermill/message"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"

	sqsextendedclient "github.com/Aryon-Security/watermill-aws/extended-client/sqs"
)

type Publisher struct {
	config PublisherConfig
	logger watermill.LoggerAdapter
	sqs    sqsextendedclient.SQSClient
}

func NewPublisher(config PublisherConfig, logger watermill.LoggerAdapter) (*Publisher, error) {
	config.setDefaults()

	if err := config.Validate(); err != nil {
		return nil, err
	}

	sqsClient := sqs.NewFromConfig(config.AWSConfig, config.SQSOptFns...)
	s3Client := s3.NewFromConfig(config.AWSConfig, config.S3OptsFns...)
	extendedSQSClient, err := sqsextendedclient.New(sqsClient, s3Client, config.ExtendedSQSOpts...)
	if err != nil {
		return nil, fmt.Errorf("cannot create extended SQS client: %w", err)
	}

	return &Publisher{
		sqs:    extendedSQSClient,
		config: config,
		logger: logger,
	}, nil
}

func (p *Publisher) Publish(topic string, messages ...*message.Message) error {
	ctx := context.Background()

	queueName, queueUrl, err := p.GetQueueUrl(ctx, topic, !p.config.DoNotCreateQueueIfNotExists)
	if err != nil {
		return fmt.Errorf("cannot get queue url: %w", err)
	}

	for _, msg := range messages {
		sqsMsg, err := p.config.Marshaler.Marshal(msg)
		if err != nil {
			return fmt.Errorf("cannot marshal message: %w", err)
		}

		p.logger.Debug("Sending message", watermill.LogFields{"msg": msg})

		input, err := p.config.GenerateSendMessageInput(ctx, queueUrl, sqsMsg)
		if err != nil {
			return fmt.Errorf("cannot generate send message input: %w", err)
		}

		_, err = p.sqs.SendMessage(ctx, input)
		var queueDoesNotExistErr *types.QueueDoesNotExist
		if errors.As(err, &queueDoesNotExistErr) && !p.config.DoNotCreateQueueIfNotExists {
			// GetQueueUrl may not create queue if QueueUrlResolver doesn't check if queue exists
			_, err := p.createQueue(ctx, topic, queueName)
			if err != nil {
				return err
			}
		}
		if err != nil {
			return fmt.Errorf("cannot send message: %w", err)
		}
	}

	return nil
}

func (p *Publisher) GetQueueUrl(ctx context.Context, topic string, createIfNotExists bool) (QueueName, QueueURL, error) {
	resolvedQueue, err := p.config.QueueUrlResolver.ResolveQueueUrl(ctx, ResolveQueueUrlParams{
		Topic:     topic,
		SqsClient: p.sqs,
		Logger:    p.logger,
	})
	if err != nil {
		return "", "", err
	}
	if resolvedQueue.Exists != nil && *resolvedQueue.Exists {
		return resolvedQueue.QueueName, *resolvedQueue.QueueURL, nil
	}

	if createIfNotExists {
		queueUrl, err := p.createQueue(ctx, topic, resolvedQueue.QueueName)
		if err != nil {
			return "", "", err
		}

		return resolvedQueue.QueueName, queueUrl, nil

	} else {
		return "", "", fmt.Errorf("queue for topic %s doesn't exist", topic)
	}
}

func (p *Publisher) createQueue(ctx context.Context, topic string, queueName QueueName) (QueueURL, error) {
	input, err := p.config.GenerateCreateQueueInput(ctx, queueName, p.config.CreateQueueConfig)
	if err != nil {
		return "", fmt.Errorf("cannot generate create queue input: %w", err)
	}

	queueUrl, err := createQueue(ctx, p.sqs, input)
	if err != nil {
		return "", fmt.Errorf("cannot create queue: %w", err)
	}
	// queue was created in the meantime
	if queueUrl == nil {
		resolvedQueue, err := p.config.QueueUrlResolver.ResolveQueueUrl(ctx, ResolveQueueUrlParams{
			Topic:     topic,
			SqsClient: p.sqs,
			Logger:    p.logger,
		})
		if err != nil {
			return "", err
		}
		if resolvedQueue.Exists != nil && !*resolvedQueue.Exists {
			return "", fmt.Errorf("queue doesn't exist after creation")
		}

		return *resolvedQueue.QueueURL, nil
	}

	return *queueUrl, nil
}

func (p *Publisher) GetQueueArn(ctx context.Context, url *QueueURL) (*QueueArn, error) {
	return getARNUrl(ctx, p.sqs, url)
}

func (p *Publisher) Close() error {
	return nil
}
