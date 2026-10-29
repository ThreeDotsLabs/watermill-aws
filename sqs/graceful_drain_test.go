package sqs_test

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	amazonsqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/stretchr/testify/require"

	"github.com/ThreeDotsLabs/watermill"
	"github.com/ThreeDotsLabs/watermill-aws/sqs"
	"github.com/ThreeDotsLabs/watermill/message"
)

// TestGracefulDrain_InFlightHandlerFinishesOnClose verifies that a subscriber
// configured with CloseTimeout drains in-flight work on shutdown: when Close()
// is called while a handler is still running, the handler keeps a live context
// and is given time to finish and ack, so the message is deleted rather than
// left for redelivery.
//
// It also cancels the context passed to Subscribe — as watermill's router does
// the instant Close() begins (router.go: <-closingInProgressCh; cancel) — to
// prove the in-flight handler's context is shielded from that cancellation.
func TestGracefulDrain_InFlightHandlerFinishesOnClose(t *testing.T) {
	t.Parallel()

	const (
		// visibilityTimeout must exceed handlerWork so the receipt handle stays
		// valid until the drained ack deletes the message.
		visibilityTimeout = "5"
		closeTimeout      = 15 * time.Second
		handlerWork       = 1500 * time.Millisecond
	)

	var deleteCalls atomic.Int32

	cfg := newAwsConfig(t)

	pub, err := sqs.NewPublisher(sqs.PublisherConfig{
		AWSConfig:         cfg,
		OptFns:            []func(*amazonsqs.Options){GetEndpointResolverSqs()},
		CreateQueueConfig: sqs.QueueConfigAttributes{VisibilityTimeout: visibilityTimeout},
		Marshaler:         sqs.DefaultMarshalerUnmarshaler{},
	}, watermill.NewStdLogger(false, false))
	require.NoError(t, err)
	defer func() { _ = pub.Close() }()

	sub, err := sqs.NewSubscriber(sqs.SubscriberConfig{
		AWSConfig:             cfg,
		OptFns:                []func(*amazonsqs.Options){GetEndpointResolverSqs()},
		QueueConfigAttributes: sqs.QueueConfigAttributes{VisibilityTimeout: visibilityTimeout},
		Unmarshaler:           sqs.DefaultMarshalerUnmarshaler{},
		CloseTimeout:          closeTimeout,
		GenerateDeleteMessageInput: func(ctx context.Context, queueURL sqs.QueueURL, receiptHandle *string) (*amazonsqs.DeleteMessageInput, error) {
			deleteCalls.Add(1)
			return sqs.GenerateDeleteMessageInputDefault(ctx, queueURL, receiptHandle)
		},
	}, watermill.NewStdLogger(false, false))
	require.NoError(t, err)

	topic := fmt.Sprintf("graceful-drain-%s", watermill.NewUUID())
	require.NoError(t, sub.SubscribeInitialize(topic))
	require.NoError(t, pub.Publish(topic, message.NewMessage(watermill.NewUUID(), []byte("payload"))))

	subCtx, subCancel := context.WithCancel(context.Background())
	defer subCancel()

	out, err := sub.Subscribe(subCtx, topic)
	require.NoError(t, err)

	var (
		received       = make(chan struct{})
		handlerAcked   = make(chan struct{})
		ctxStayedAlive atomic.Bool
		receivedCount  atomic.Int32
	)

	go func() {
		msg, ok := <-out
		if !ok {
			return
		}
		receivedCount.Add(1)
		close(received)

		// Work that spans the shutdown the main goroutine triggers below.
		// If the drain works, the message context stays alive for the whole
		// window; otherwise it is cancelled and we fall into the other case.
		select {
		case <-time.After(handlerWork):
			ctxStayedAlive.Store(true)
		case <-msg.Context().Done():
			ctxStayedAlive.Store(false)
		}

		msg.Ack()
		close(handlerAcked)
	}()

	select {
	case <-received:
	case <-time.After(20 * time.Second):
		t.Fatal("did not receive the published message")
	}

	// Shutdown: cancel the Subscribe context (as the router does at Close) and
	// close the subscriber. Neither must abort the in-flight handler.
	subCancel()
	closeReturned := make(chan error, 1)
	go func() { closeReturned <- sub.Close() }()

	select {
	case <-handlerAcked:
	case <-time.After(closeTimeout + 5*time.Second):
		t.Fatal("in-flight handler did not finish during drain")
	}

	require.True(t, ctxStayedAlive.Load(),
		"handler context was cancelled during drain; in-flight work was aborted")

	select {
	case err := <-closeReturned:
		require.NoError(t, err)
	case <-time.After(closeTimeout + 5*time.Second):
		t.Fatal("Close did not return after drain")
	}

	require.EqualValues(t, 1, receivedCount.Load(), "message should be delivered exactly once")
	require.GreaterOrEqual(t, deleteCalls.Load(), int32(1),
		"drained ack should delete the message so it is not redelivered")

	// End-to-end proof of non-redelivery: a fresh subscriber must not see the
	// message reappear within a couple of visibility windows.
	verifyCtx, verifyCancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer verifyCancel()

	sub2, err := sqs.NewSubscriber(sqs.SubscriberConfig{
		AWSConfig:             cfg,
		OptFns:                []func(*amazonsqs.Options){GetEndpointResolverSqs()},
		QueueConfigAttributes: sqs.QueueConfigAttributes{VisibilityTimeout: visibilityTimeout},
		Unmarshaler:           sqs.DefaultMarshalerUnmarshaler{},
	}, watermill.NewStdLogger(false, false))
	require.NoError(t, err)
	defer func() { _ = sub2.Close() }()

	out2, err := sub2.Subscribe(verifyCtx, topic)
	require.NoError(t, err)

	select {
	case msg, ok := <-out2:
		if ok {
			t.Fatalf("message was redelivered after a drained ack: %s", msg.UUID)
		}
	case <-verifyCtx.Done():
		// No redelivery within the window: the drained ack really removed it.
	}
}

// TestGracefulDrain_Disabled_AbortsInFlightOnClose documents the opt-out: with
// CloseTimeout == 0 (the default), closing the subscriber cancels the in-flight
// handler's context immediately, preserving the previous abort-and-redeliver
// behavior for callers that have not opted into draining.
func TestGracefulDrain_Disabled_AbortsInFlightOnClose(t *testing.T) {
	t.Parallel()

	cfg := newAwsConfig(t)

	pub, err := sqs.NewPublisher(sqs.PublisherConfig{
		AWSConfig:         cfg,
		OptFns:            []func(*amazonsqs.Options){GetEndpointResolverSqs()},
		CreateQueueConfig: sqs.QueueConfigAttributes{VisibilityTimeout: "30"},
		Marshaler:         sqs.DefaultMarshalerUnmarshaler{},
	}, watermill.NewStdLogger(false, false))
	require.NoError(t, err)
	defer func() { _ = pub.Close() }()

	sub, err := sqs.NewSubscriber(sqs.SubscriberConfig{
		AWSConfig:             cfg,
		OptFns:                []func(*amazonsqs.Options){GetEndpointResolverSqs()},
		QueueConfigAttributes: sqs.QueueConfigAttributes{VisibilityTimeout: "30"},
		Unmarshaler:           sqs.DefaultMarshalerUnmarshaler{},
		// CloseTimeout left at 0: draining disabled.
	}, watermill.NewStdLogger(false, false))
	require.NoError(t, err)

	topic := fmt.Sprintf("graceful-drain-off-%s", watermill.NewUUID())
	require.NoError(t, sub.SubscribeInitialize(topic))
	require.NoError(t, pub.Publish(topic, message.NewMessage(watermill.NewUUID(), []byte("payload"))))

	out, err := sub.Subscribe(context.Background(), topic)
	require.NoError(t, err)

	var (
		received     = make(chan struct{})
		ctxCancelled = make(chan struct{})
	)

	go func() {
		msg, ok := <-out
		if !ok {
			return
		}
		close(received)
		select {
		case <-msg.Context().Done():
			close(ctxCancelled) // aborted by close, as expected when draining is off
		case <-time.After(20 * time.Second):
		}
		msg.Nack()
	}()

	select {
	case <-received:
	case <-time.After(20 * time.Second):
		t.Fatal("did not receive the published message")
	}

	go func() { _ = sub.Close() }()

	select {
	case <-ctxCancelled:
		// Expected: with draining disabled, close aborts the in-flight handler.
	case <-time.After(10 * time.Second):
		t.Fatal("handler context was not cancelled on close with CloseTimeout=0")
	}
}
