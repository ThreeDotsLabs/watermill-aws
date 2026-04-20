package sns_test

import (
	"context"
	"fmt"
	"net/url"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	amazons3 "github.com/aws/aws-sdk-go-v2/service/s3"
	amazonsns "github.com/aws/aws-sdk-go-v2/service/sns"
	amazonsqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	transport "github.com/aws/smithy-go/endpoints"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ThreeDotsLabs/watermill"
	"github.com/ThreeDotsLabs/watermill/message"
	"github.com/ThreeDotsLabs/watermill/pubsub/tests"

	"github.com/Aryon-Security/watermill-aws/sqs"

	"github.com/Aryon-Security/watermill-aws/sns"
)

func TestPublishSubscribe(t *testing.T) {
	t.Parallel()

	tests.TestPubSub(
		t,
		tests.Features{
			ConsumerGroups:      true,
			ExactlyOnceDelivery: false,
			GuaranteedOrder:     false,
			Persistent:          true,
			// Currently none of emulators are stable enough to
			// handle all tests, see: https://github.com/localstack/localstack/issues/2074
			ForceShort: true,
		},
		createPubSub,
		createPubSubWithConsumerGroup,
	)
}

func TestPubSub_arn_topic_resolver(t *testing.T) {
	t.Parallel()

	tests.TestPublishSubscribe(
		t,
		tests.TestContext{
			TestID: tests.NewTestID(),
			Features: tests.Features{
				ConsumerGroups:                      true,
				ExactlyOnceDelivery:                 false,
				GuaranteedOrder:                     true,
				GuaranteedOrderWithSingleSubscriber: true,
				Persistent:                          true,
				GenerateTopicFunc: func(tctx tests.TestContext) string {
					return fmt.Sprintf("arn:aws:sns:us-west-2:000000000000:%s", tctx.TestID)
				},
				// Currently none of emulators are stable enough to
				// handle all tests, see: https://github.com/localstack/localstack/issues/2074
				ForceShort: true,
			},
		},
		func(t *testing.T) (message.Publisher, message.Subscriber) {
			cfg := GetAWSConfig(t)

			return createPubSubWithConfig(
				t,
				sns.PublisherConfig{
					AWSConfig:         cfg,
					SNSOptFns:         GetEndpointResolverSns(),
					CreateTopicConfig: sns.ConfigAttributes{},
					Marshaler:         sns.DefaultMarshalerUnmarshaler{},
					TopicResolver:     sns.TransparentTopicResolver{},
				},
				sns.SubscriberConfig{
					AWSConfig:            cfg,
					SNSOptFns:            GetEndpointResolverSns(),
					GenerateSqsQueueName: sns.GenerateSqsQueueNameEqualToTopicName,
					TopicResolver:        sns.TransparentTopicResolver{},
					// Add delay for subscription propagation in CI/localstack
					SubscriptionPropagationDelay: 10000 * time.Millisecond,
				},
				sqs.SubscriberConfig{
					AWSConfig: cfg,
					SQSOptFns: GetEndpointResolverSqs(),
					S3OptsFns: GetEndpointResolverS3(),
					QueueConfigAttributes: sqs.QueueConfigAttributes{
						// Default value is 30 seconds - need to be lower for tests
						VisibilityTimeout: "1",
					},
				},
			)
		},
	)
}

func TestPublisher_CreateTopic_is_idempotent(t *testing.T) {
	t.Parallel()

	pub, _ := createPubSub(t)

	topicName := watermill.NewUUID()

	arn1, err := pub.(*sns.Publisher).CreateTopic(context.Background(), topicName)
	require.NoError(t, err)

	arn2, err := pub.(*sns.Publisher).CreateTopic(context.Background(), topicName)
	require.NoError(t, err)

	assert.Equal(t, arn1, arn2)
}

func TestSubscriber_SubscribeInitialize_is_idempotent(t *testing.T) {
	t.Parallel()

	_, sub := createPubSub(t)

	topicName := watermill.NewUUID()

	err := sub.(*sns.Subscriber).SubscribeInitialize(topicName)
	require.NoError(t, err)

	err = sub.(*sns.Subscriber).SubscribeInitialize(topicName)
	require.NoError(t, err)
}

func createPubSub(t *testing.T) (message.Publisher, message.Subscriber) {
	cfg := GetAWSConfig(t)

	topicResolver, err := sns.NewGenerateArnTopicResolver("000000000000", "us-west-2")
	require.NoError(t, err)

	return createPubSubWithConfig(
		t,
		sns.PublisherConfig{
			AWSConfig:         cfg,
			SNSOptFns:         GetEndpointResolverSns(),
			CreateTopicConfig: sns.ConfigAttributes{},
			TopicResolver:     topicResolver,
			Marshaler:         sns.DefaultMarshalerUnmarshaler{},
		},
		sns.SubscriberConfig{
			AWSConfig:            cfg,
			SNSOptFns:            GetEndpointResolverSns(),
			TopicResolver:        topicResolver,
			GenerateSqsQueueName: sns.GenerateSqsQueueNameEqualToTopicName,
		},
		sqs.SubscriberConfig{
			AWSConfig: cfg,
			SQSOptFns: GetEndpointResolverSqs(),
			S3OptsFns: GetEndpointResolverS3(),
			QueueConfigAttributes: sqs.QueueConfigAttributes{
				// Default value is 30 seconds - need to be lower for tests
				VisibilityTimeout: "1",
			},
		},
	)
}

func createPubSubWithConsumerGroup(t *testing.T, consumerGroup string) (message.Publisher, message.Subscriber) {
	cfg := GetAWSConfig(t)

	topicResolver, err := sns.NewGenerateArnTopicResolver("000000000000", "us-west-2")
	require.NoError(t, err)

	return createPubSubWithConfig(
		t,
		sns.PublisherConfig{
			AWSConfig:         cfg,
			SNSOptFns:         GetEndpointResolverSns(),
			CreateTopicConfig: sns.ConfigAttributes{},
			Marshaler:         sns.DefaultMarshalerUnmarshaler{},
			TopicResolver:     topicResolver,
		},
		sns.SubscriberConfig{
			AWSConfig: cfg,
			SNSOptFns: GetEndpointResolverSns(),
			GenerateSqsQueueName: func(ctx context.Context, sqsTopic sns.TopicArn) (string, error) {
				return consumerGroup, nil
			},
			TopicResolver: topicResolver,
		},
		sqs.SubscriberConfig{
			AWSConfig: cfg,
			SQSOptFns: GetEndpointResolverSqs(),
			S3OptsFns: GetEndpointResolverS3(),
			QueueConfigAttributes: sqs.QueueConfigAttributes{
				// Default value is 30 seconds - need to be lower for tests
				VisibilityTimeout: "1",
			},
		},
	)
}

func createPubSubWithConfig(
	t *testing.T,
	pubConfig sns.PublisherConfig,
	subConfig sns.SubscriberConfig,
	sqsSubConfig sqs.SubscriberConfig,
) (message.Publisher, message.Subscriber) {
	logger := watermill.NewStdLogger(true, false)

	pub, err := sns.NewPublisher(pubConfig, logger)
	require.NoError(t, err)

	sub, err := sns.NewSubscriber(subConfig, sqsSubConfig, logger)
	require.NoError(t, err)

	return pub, sub
}

func GetAWSConfig(t *testing.T) aws.Config {
	t.Helper()

	cfg, err := awsconfig.LoadDefaultConfig(
		context.Background(),
		awsconfig.WithRegion("us-west-2"),
		awsconfig.WithCredentialsProvider(credentials.StaticCredentialsProvider{
			Value: aws.Credentials{
				AccessKeyID:     "test",
				SecretAccessKey: "test",
			},
		}),
	)
	require.NoError(t, err)

	return cfg
}

func GetEndpointResolverSns() []func(*amazonsns.Options) {
	return []func(*amazonsns.Options){
		amazonsns.WithEndpointResolverV2(sns.OverrideEndpointResolver{
			Endpoint: transport.Endpoint{
				URI: url.URL{Scheme: "http", Host: "localhost:4566"},
			},
		}),
	}
}

func GetEndpointResolverSqs() []func(*amazonsqs.Options) {
	return []func(*amazonsqs.Options){
		amazonsqs.WithEndpointResolverV2(sqs.OverrideEndpointResolver{
			Endpoint: transport.Endpoint{
				URI: url.URL{Scheme: "http", Host: "localhost:4566"},
			},
		}),
	}
}

func GetEndpointResolverS3() []func(*amazons3.Options) {
	return []func(*amazons3.Options){
		amazons3.WithEndpointResolverV2(sqs.S3OverrideEndpointResolver{
			Endpoint: transport.Endpoint{
				URI: url.URL{Scheme: "http", Host: "localhost:4566"},
			},
		}),
		func(options *amazons3.Options) {
			options.UsePathStyle = true
		},
	}
}
