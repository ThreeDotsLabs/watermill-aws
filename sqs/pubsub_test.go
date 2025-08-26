package sqs_test

import (
	"context"
	"fmt"
	"net/url"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	amazonsqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	transport "github.com/aws/smithy-go/endpoints"
	"github.com/stretchr/testify/require"

	"github.com/ThreeDotsLabs/watermill"
	"github.com/ThreeDotsLabs/watermill-aws/sqs"
	"github.com/ThreeDotsLabs/watermill/message"
	"github.com/ThreeDotsLabs/watermill/pubsub/tests"
)

func TestPubSub(t *testing.T) {
	t.Parallel()

	tests.TestPubSub(
		t,
		tests.Features{
			ConsumerGroups:      false,
			ExactlyOnceDelivery: false,
			GuaranteedOrder:     true,
			Persistent:          true,
		},
		createPubSub,
		createPubSubWithConsumerGroup,
	)
}

func TestPublishSubscribe_with_GenerateQueueUrlResolver(t *testing.T) {
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
			},
		},
		func(t *testing.T) (message.Publisher, message.Subscriber) {
			cfg := newAwsConfig(t)

			queueResolver := sqs.GenerateQueueUrlResolver{
				AwsRegion:    "us-west-2",
				AwsAccountID: "000000000000",
			}

			return createPubSubWithConfig(
				t,
				sqs.PublisherConfig{
					AWSConfig: cfg,
					OptFns: []func(*amazonsqs.Options){
						GetEndpointResolverSqs(),
					},
					CreateQueueConfig: sqs.QueueConfigAttributes{
						// Default value is 30 seconds - need to be lower for tests
						VisibilityTimeout: "1",
					},
					Marshaler:        sqs.DefaultMarshalerUnmarshaler{},
					QueueUrlResolver: queueResolver,
				},
				sqs.SubscriberConfig{
					AWSConfig: cfg,
					OptFns: []func(*amazonsqs.Options){
						GetEndpointResolverSqs(),
					},
					QueueConfigAttributes: sqs.QueueConfigAttributes{
						// Default value is 30 seconds - need to be lower for tests
						VisibilityTimeout: "1",
					},
					GenerateReceiveMessageInput: func(ctx context.Context, queueURL sqs.QueueURL) (*awssqs.ReceiveMessageInput, error) {
						in, err := sqs.GenerateReceiveMessageInputDefault(ctx, queueURL)
						if err != nil {
							return nil, err
						}

						// this will effectively enable batching
						in.MaxNumberOfMessages = 10

						return in, nil
					},
					Unmarshaler:      sqs.DefaultMarshalerUnmarshaler{},
					QueueUrlResolver: queueResolver,
				},
			)
		},
	)
}

func TestPublishSubscribe_with_TransparentUrlResolver(t *testing.T) {
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
					return fmt.Sprintf("http://sqs.us-west-2.localhost.localstack.cloud:4566/000000000000/%s", tctx.TestID)
				},
			},
		},
		func(t *testing.T) (message.Publisher, message.Subscriber) {
			cfg := newAwsConfig(t)

			queueResolver := sqs.TransparentUrlResolver{}

			return createPubSubWithConfig(
				t,
				sqs.PublisherConfig{
					AWSConfig: cfg,
					OptFns: []func(*amazonsqs.Options){
						GetEndpointResolverSqs(),
					},
					CreateQueueConfig: sqs.QueueConfigAttributes{
						// Default value is 30 seconds - need to be lower for tests
						VisibilityTimeout: "1",
					},
					Marshaler:        sqs.DefaultMarshalerUnmarshaler{},
					QueueUrlResolver: queueResolver,
				},
				sqs.SubscriberConfig{
					AWSConfig: cfg,
					OptFns: []func(*amazonsqs.Options){
						GetEndpointResolverSqs(),
					},
					QueueConfigAttributes: sqs.QueueConfigAttributes{
						// Default value is 30 seconds - need to be lower for tests
						VisibilityTimeout: "1",
					},
					GenerateReceiveMessageInput: func(ctx context.Context, queueURL sqs.QueueURL) (*awssqs.ReceiveMessageInput, error) {
						in, err := sqs.GenerateReceiveMessageInputDefault(ctx, queueURL)
						if err != nil {
							return nil, err
						}

						// this will effectively enable batching
						in.MaxNumberOfMessages = 10

						return in, nil
					},
					Unmarshaler:      sqs.DefaultMarshalerUnmarshaler{},
					QueueUrlResolver: queueResolver,
				},
			)
		},
	)
}

func TestPublishSubscribe_batching(t *testing.T) {
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
			},
		},
		func(t *testing.T) (message.Publisher, message.Subscriber) {
			cfg := newAwsConfig(t)

			return createPubSubWithConfig(
				t,
				sqs.PublisherConfig{
					AWSConfig: cfg,
					OptFns: []func(*amazonsqs.Options){
						GetEndpointResolverSqs(),
					},
					CreateQueueConfig: sqs.QueueConfigAttributes{
						// Default value is 30 seconds - need to be lower for tests
						VisibilityTimeout: "1",
					},
					Marshaler: sqs.DefaultMarshalerUnmarshaler{},
				},
				sqs.SubscriberConfig{
					AWSConfig: cfg,
					OptFns: []func(*amazonsqs.Options){
						GetEndpointResolverSqs(),
					},
					QueueConfigAttributes: sqs.QueueConfigAttributes{
						// Default value is 30 seconds - need to be lower for tests
						VisibilityTimeout: "1",
					},
					GenerateReceiveMessageInput: func(ctx context.Context, queueURL sqs.QueueURL) (*awssqs.ReceiveMessageInput, error) {
						in, err := sqs.GenerateReceiveMessageInputDefault(ctx, queueURL)
						if err != nil {
							return nil, err
						}

						// this will effectively enable batching
						in.MaxNumberOfMessages = 10

						return in, nil
					},
					Unmarshaler: sqs.DefaultMarshalerUnmarshaler{},
				},
			)
		},
	)
}

func TestPublishSubscribe_creating_queue_with_different_settings_should_be_idempotent(t *testing.T) {
	t.Parallel()

	logger := watermill.NewStdLogger(false, false)

	sub1, err := sqs.NewSubscriber(sqs.SubscriberConfig{
		AWSConfig: newAwsConfig(t),
		OptFns: []func(*amazonsqs.Options){
			GetEndpointResolverSqs(),
		},
		QueueConfigAttributes: sqs.QueueConfigAttributes{
			VisibilityTimeout: "1",
		},
		Unmarshaler: sqs.DefaultMarshalerUnmarshaler{},
	}, logger)
	require.NoError(t, err)

	sub2, err := sqs.NewSubscriber(sqs.SubscriberConfig{
		AWSConfig: newAwsConfig(t),
		OptFns: []func(*amazonsqs.Options){
			GetEndpointResolverSqs(),
		},
		QueueConfigAttributes: sqs.QueueConfigAttributes{
			VisibilityTimeout: "20",
		},
		Unmarshaler: sqs.DefaultMarshalerUnmarshaler{},
	}, logger)
	require.NoError(t, err)

	topicName := watermill.NewUUID()

	require.NoError(t, sub1.SubscribeInitialize(topicName))
	require.NoError(t, sub2.SubscribeInitialize(topicName))
}

func TestPublisher_GetOrCreateQueueUrl_is_idempotent(t *testing.T) {
	t.Parallel()

	pub, _ := createPubSub(t)

	topicName := watermill.NewUUID()

	name1, url1, err := pub.(*sqs.Publisher).GetQueueUrl(context.Background(), topicName, true)
	require.NoError(t, err)

	name2, url2, err := pub.(*sqs.Publisher).GetQueueUrl(context.Background(), topicName, true)
	require.NoError(t, err)

	require.Equal(t, url1, url2)
	require.Equal(t, name1, name2)

}

func TestSubscriber_doesnt_hang_when_queue_doesnt_exist(t *testing.T) {
	t.Parallel()

	cfg := newAwsConfig(t)

	_, sub := createPubSubWithConfig(
		t,
		sqs.PublisherConfig{
			AWSConfig: cfg,
			OptFns: []func(*amazonsqs.Options){
				GetEndpointResolverSqs(),
			},
			CreateQueueConfig: sqs.QueueConfigAttributes{
				// Default value is 30 seconds - need to be lower for tests
				VisibilityTimeout: "1",
			},
			Marshaler: sqs.DefaultMarshalerUnmarshaler{},
		},
		sqs.SubscriberConfig{
			AWSConfig: cfg,
			OptFns: []func(*amazonsqs.Options){
				GetEndpointResolverSqs(),
			},
			QueueConfigAttributes: sqs.QueueConfigAttributes{
				// Default value is 30 seconds - need to be lower for tests
				VisibilityTimeout: "1",
			},
			Unmarshaler:                 sqs.DefaultMarshalerUnmarshaler{},
			DoNotCreateQueueIfNotExists: true,
		},
	)
	msgs, err := sub.Subscribe(context.Background(), "non-existing-queue")

	require.ErrorContains(t, err, "queue for topic 'non-existing-queue' doesn't exists")
	require.Nil(t, msgs)
}

func TestPublisher_do_not_create_queue(t *testing.T) {
	t.Parallel()

	cfg := newAwsConfig(t)

	pub, _ := createPubSubWithConfig(
		t,
		sqs.PublisherConfig{
			AWSConfig: cfg,
			OptFns: []func(*amazonsqs.Options){
				GetEndpointResolverSqs(),
			},
			CreateQueueConfig: sqs.QueueConfigAttributes{
				// Default value is 30 seconds - need to be lower for tests
				VisibilityTimeout: "1",
			},
			Marshaler:                   sqs.DefaultMarshalerUnmarshaler{},
			DoNotCreateQueueIfNotExists: true,
		},
		sqs.SubscriberConfig{
			AWSConfig: cfg,
			OptFns: []func(*amazonsqs.Options){
				GetEndpointResolverSqs(),
			},
			QueueConfigAttributes: sqs.QueueConfigAttributes{
				// Default value is 30 seconds - need to be lower for tests
				VisibilityTimeout: "1",
			},
			Unmarshaler: sqs.DefaultMarshalerUnmarshaler{},
		},
	)
	err := pub.Publish("non-existing-queue-2", message.NewMessage("1", []byte("x")))

	require.ErrorContains(t, err, "queue for topic non-existing-queue-2 doesn't exist")
}

func createPubSub(t *testing.T) (message.Publisher, message.Subscriber) {
	cfg := newAwsConfig(t)

	return createPubSubWithConfig(
		t,
		sqs.PublisherConfig{
			AWSConfig: cfg,
			OptFns: []func(*amazonsqs.Options){
				GetEndpointResolverSqs(),
			},
			CreateQueueConfig: sqs.QueueConfigAttributes{
				// Default value is 30 seconds - need to be lower for tests
				VisibilityTimeout: "1",
			},
			Marshaler: sqs.DefaultMarshalerUnmarshaler{},
		},
		sqs.SubscriberConfig{
			AWSConfig: cfg,
			OptFns: []func(*amazonsqs.Options){
				GetEndpointResolverSqs(),
			},
			QueueConfigAttributes: sqs.QueueConfigAttributes{
				// Default value is 30 seconds - need to be lower for tests
				VisibilityTimeout: "1",
			},
			Unmarshaler: sqs.DefaultMarshalerUnmarshaler{},
		},
	)
}

func createPubSubWithConfig(t *testing.T, pubConfig sqs.PublisherConfig, subConfig sqs.SubscriberConfig) (message.Publisher, message.Subscriber) {
	logger := watermill.NewStdLogger(false, false)

	pub, err := sqs.NewPublisher(pubConfig, logger)
	require.NoError(t, err)

	sub, err := sqs.NewSubscriber(subConfig, logger)
	require.NoError(t, err)

	return pub, sub
}

func newAwsConfig(t *testing.T) aws.Config {
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

func createPubSubWithConsumerGroup(t *testing.T, consumerGroup string) (message.Publisher, message.Subscriber) {
	return createPubSub(t)
}

func GetEndpointResolverSqs() func(*amazonsqs.Options) {
	return amazonsqs.WithEndpointResolverV2(sqs.OverrideEndpointResolver{
		Endpoint: transport.Endpoint{
			URI: url.URL{Scheme: "http", Host: "localhost:4566"},
		},
	})
}
