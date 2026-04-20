package sns

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/ThreeDotsLabs/watermill"
	"github.com/ThreeDotsLabs/watermill/message"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	awsSqs "github.com/aws/aws-sdk-go-v2/service/sqs"

	snsextendedclient "github.com/Aryon-Security/watermill-aws/extended-client/sns"
	"github.com/Aryon-Security/watermill-aws/sqs"
)

type Subscriber struct {
	config SubscriberConfig
	logger watermill.LoggerAdapter

	sns snsextendedclient.SNSClient
	sqs *sqs.Subscriber
}

func NewSubscriber(
	config SubscriberConfig,
	sqsConfig sqs.SubscriberConfig,
	logger watermill.LoggerAdapter,
) (*Subscriber, error) {
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

	sqs, err := sqs.NewSubscriber(sqsConfig, logger)
	if err != nil {
		return nil, fmt.Errorf("cannot create SQS subscriber: %w", err)
	}

	snsClient := sns.NewFromConfig(config.AWSConfig, config.SNSOptFns...)
	s3Client := s3.NewFromConfig(config.AWSConfig, config.S3OptsFns...)
	extendedSnsClient, err := snsextendedclient.New(snsClient, s3Client, config.ExtendedSNSOpts...)
	if err != nil {
		return nil, fmt.Errorf("cannot create extended SQS client: %w", err)
	}

	return &Subscriber{
		config: config,
		logger: logger,
		sns:    extendedSnsClient,
		sqs:    sqs,
	}, nil
}

func (s *Subscriber) Subscribe(ctx context.Context, topic string) (<-chan *message.Message, error) {
	snsTopicArn, err := s.config.TopicResolver.ResolveTopic(ctx, topic)
	if err != nil {
		return nil, err
	}

	logger := s.logger.With(watermill.LogFields{
		"topic":     topic,
		"topic_arn": snsTopicArn,
	})
	logger.Debug("Subscribing to topic", nil)

	if !s.config.DoNotCreateSqsSubscription {
		if err := s.SubscribeInitializeWithContext(ctx, topic); err != nil {
			return nil, err
		}
	}

	sqsTopic, err := s.config.GenerateSqsQueueName(ctx, snsTopicArn)
	if err != nil {
		return nil, fmt.Errorf("failed to generate SQS queue name: %w", err)
	}

	logger.Debug("Subscribing to SQS", watermill.LogFields{"sqs_topic": sqsTopic})

	return s.sqs.Subscribe(ctx, sqsTopic)
}

// SubscribeInitialize initializes SNS subscription for given topic.
// It creates SQS queue and subscribes it to SNS topic.
func (s *Subscriber) SubscribeInitialize(topic string) error {
	return s.SubscribeInitializeWithContext(context.Background(), topic)
}

// SubscribeInitializeWithContext initializes SNS subscription for given topic.
// It creates SQS queue and subscribes it to SNS topic.
func (s *Subscriber) SubscribeInitializeWithContext(ctx context.Context, topic string) error {
	logger := s.logger.With(watermill.LogFields{
		"topic": topic,
	})
	logger.Debug("Initializing SNS subscription", nil)

	snsTopicArn, err := s.config.TopicResolver.ResolveTopic(ctx, topic)
	if err != nil {
		return err
	}

	logger.Debug("Resolved topic", watermill.LogFields{"topic_arn": snsTopicArn})

	sqsTopic, err := s.config.GenerateSqsQueueName(ctx, snsTopicArn)
	if err != nil {
		return fmt.Errorf("failed to generate SQS queue name: %w", err)
	}

	if err := s.sqs.SubscribeInitializeWithContext(ctx, sqsTopic); err != nil {
		return fmt.Errorf("cannot initialize SQS subscription for topic %s: %w", snsTopicArn, err)
	}

	sqsURL, err := s.sqs.GetQueueUrl(ctx, sqsTopic)
	if err != nil {
		return fmt.Errorf("cannot get queue url for topic %s: %w", snsTopicArn, err)
	}
	sqsQueueArn, err := s.sqs.GetQueueArn(ctx, sqsURL)
	if err != nil {
		return fmt.Errorf("cannot get queue ARN for topic %s: %w", snsTopicArn, err)
	}

	if !s.config.DoNotSetQueueAccessPolicy {
		if err := s.setSqsQueuePolicy(ctx, *sqsQueueArn, snsTopicArn, *sqsURL); err != nil {
			return fmt.Errorf("cannot set queue access policy for topic %s: %w", snsTopicArn, err)
		}
	}

	s.logger.Info("Subscribing to SNS", watermill.LogFields{
		"sns_topic_arn": snsTopicArn,
		"sqs_topic":     sqsTopic,
		"sqs_url":       *sqsURL,
		"sqs_arn":       *sqsQueueArn,
	})

	input, err := s.config.GenerateSubscribeInput(ctx, GenerateSubscribeInputParams{
		SqsTopic:    sqsTopic,
		SnsTopicArn: snsTopicArn,
		SqsQueueArn: *sqsQueueArn,
	})
	if err != nil {
		return fmt.Errorf("cannot generate subscribe input for SNS[%s] from %s: %w", snsTopicArn, *sqsQueueArn, err)
	}

	subscribeOutput, err := s.sns.Subscribe(ctx, input)
	if err != nil || subscribeOutput == nil {
		return fmt.Errorf("cannot subscribe to SNS[%s] from %s: %w", snsTopicArn, *sqsQueueArn, err)
	}

	// Wait for subscription to propagate if configured
	if s.config.SubscriptionPropagationDelay > 0 {
		s.logger.Debug("Waiting for subscription to propagate", watermill.LogFields{
			"delay": s.config.SubscriptionPropagationDelay.String(),
		})
		time.Sleep(s.config.SubscriptionPropagationDelay)
	}

	return nil
}

func (s *Subscriber) setSqsQueuePolicy(ctx context.Context, sqsQueueArn sqs.QueueArn, snsTopicArn TopicArn, sqsURL sqs.QueueURL) error {
	policy, err := s.config.GenerateQueueAccessPolicy(ctx, GenerateQueueAccessPolicyParams{
		SqsQueueArn: sqsQueueArn,
		SnsTopicArn: snsTopicArn,
		SqsURL:      sqsURL,
	})
	if err != nil {
		return fmt.Errorf("cannot generate queue access policy: %w", err)
	}

	policyJSON, err := json.Marshal(policy)
	if err != nil {
		return fmt.Errorf("cannot marshal policy: %w", err)
	}

	s.logger.Debug("Setting queue access policy", watermill.LogFields{
		"policy": string(policyJSON),
	})

	_, err = s.sqs.GetClient().SetQueueAttributes(ctx, &awsSqs.SetQueueAttributesInput{
		QueueUrl: aws.String(string(sqsURL)),
		Attributes: map[string]string{
			"Policy": string(policyJSON),
		},
	})
	if err != nil {
		return fmt.Errorf("cannot set queue policy: %w", err)
	}
	return nil
}

func (s *Subscriber) Close() error {
	return s.sqs.Close()
}
