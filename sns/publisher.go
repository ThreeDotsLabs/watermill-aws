package sns

import (
	"context"
	"errors"
	"fmt"

	"github.com/ThreeDotsLabs/watermill"
	"github.com/ThreeDotsLabs/watermill/message"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/aws/aws-sdk-go-v2/service/sns/types"

	snsextendedclient "github.com/Aryon-Security/watermill-aws/extended-client/sns"
)

type Publisher struct {
	config PublisherConfig
	logger watermill.LoggerAdapter
	sns    snsextendedclient.SNSClient
}

func NewPublisher(config PublisherConfig, logger watermill.LoggerAdapter) (*Publisher, error) {
	config.setDefaults()

	if err := config.Validate(); err != nil {
		return nil, err
	}

	snsClient := sns.NewFromConfig(config.AWSConfig, config.SNSOptFns...)
	s3Client := s3.NewFromConfig(config.AWSConfig, config.S3OptsFns...)
	extendedSnsClient, err := snsextendedclient.New(snsClient, s3Client, config.ExtendedSNSOpts...)
	if err != nil {
		return nil, fmt.Errorf("cannot create extended SQS client: %w", err)
	}

	return &Publisher{
		sns:    extendedSnsClient,
		config: config,
		logger: logger,
	}, nil
}

func (p *Publisher) Publish(topic string, messages ...*message.Message) error {
	ctx := context.Background()

	topicArn, err := p.config.TopicResolver.ResolveTopic(ctx, topic)
	if err != nil {
		return err
	}

	for _, msg := range messages {
		p.logger.Debug("Sending message", watermill.LogFields{
			"msg":       msg,
			"topic_arn": topicArn,
		})
		// Real messageId are generated on server side
		// so we can set our own here so we can use it in the tests
		// There is a deduplicationId but just for FIFO queues
		input := p.config.Marshaler.Marshal(topicArn, msg)
		_, err := p.sns.Publish(ctx, input)

		var topicNotFoundError *types.NotFoundException
		if errors.As(err, &topicNotFoundError) && !p.config.DoNotCreateTopicIfNotExists {
			// in most cases topic will already exist - as form of optimisation we
			// assume that topic exists to avoid unnecessary API calls
			// we create topic only if it doesn't exist
			if _, err := p.CreateTopic(ctx, topic); err != nil {
				return fmt.Errorf("failed to create topic: %w", err)
			}

			// now topic should exist
			_, err = p.sns.Publish(ctx, input)
		}
		if err != nil {
			return fmt.Errorf("cannot publish message to topic %s: %w", topicArn, err)
		}
	}

	return nil
}

func (p *Publisher) CreateTopic(ctx context.Context, topic string) (string, error) {
	topicArn, err := p.config.TopicResolver.ResolveTopic(ctx, topic)
	if err != nil {
		return "", err
	}

	topicName, err := ExtractTopicNameFromTopicArn(topicArn)
	if err != nil {
		return "", err
	}

	input, err := p.config.GenerateCreateTopicInput(ctx, topicName, p.config.CreateTopicConfig)
	if err != nil {
		return "", fmt.Errorf("cannot generate create topic input for %s: %w", topicName, err)
	}

	createdTopicArn, err := createSnsTopic(ctx, p.sns, input)
	if err != nil {
		return "", fmt.Errorf("failed to create SNS topic: %w", err)
	}
	if createdTopicArn == nil {
		return "", fmt.Errorf("created topic arn is nil")
	}

	if *createdTopicArn != string(topicArn) {
		return "", fmt.Errorf(
			"created topic arn (%s) is not equal to expected topic arn (%s), please check the configuration",
			*createdTopicArn,
			topicArn,
		)
	}

	return *createdTopicArn, nil
}

func (p *Publisher) Close() error {
	return nil
}
