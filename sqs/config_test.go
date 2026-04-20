package sqs_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Aryon-Security/watermill-aws/sqs"
)

func TestSubscriberConfig_SetDefaults_ConsumeWorkers(t *testing.T) {
	t.Run("defaults to 1 when not set", func(t *testing.T) {
		cfg := sqs.SubscriberConfig{}
		cfg.SetDefaults()
		require.Equal(t, 1, cfg.ConsumeWorkers)
	})

	t.Run("defaults to 1 when set to 0", func(t *testing.T) {
		cfg := sqs.SubscriberConfig{ConsumeWorkers: 0}
		cfg.SetDefaults()
		require.Equal(t, 1, cfg.ConsumeWorkers)
	})

	t.Run("defaults to 1 when negative", func(t *testing.T) {
		cfg := sqs.SubscriberConfig{ConsumeWorkers: -5}
		cfg.SetDefaults()
		require.Equal(t, 1, cfg.ConsumeWorkers)
	})

	t.Run("preserves explicit positive value", func(t *testing.T) {
		cfg := sqs.SubscriberConfig{ConsumeWorkers: 10}
		cfg.SetDefaults()
		require.Equal(t, 10, cfg.ConsumeWorkers)
	})
}

func TestQueueConfigAttributes_Attributes(t *testing.T) {
	structAttrs := sqs.QueueConfigAttributes{
		DelaySeconds:                  "10",
		MaximumMessageSize:            "20",
		MessageRetentionPeriod:        "20",
		Policy:                        "test",
		ReceiveMessageWaitTimeSeconds: "30",
		RedrivePolicy:                 "test",
		DeadLetterTargetArn:           "test",
		FifoQueue:                     false,
		ContentBasedDeduplication:     true,
	}

	attrs, err := structAttrs.Attributes()
	require.NoError(t, err)

	require.Equal(
		t,
		map[string]string{
			"ContentBasedDeduplication":     "true",
			"DelaySeconds":                  "10",
			"MaximumMessageSize":            "20",
			"MessageRetentionPeriod":        "20",
			"Policy":                        "test",
			"ReceiveMessageWaitTimeSeconds": "30",
			"RedrivePolicy":                 "test",
			"deadLetterTargetArn":           "test",
		},
		attrs,
	)
}

func TestQueueConfigAttributes_Attributes_custom_attributes(t *testing.T) {
	structAttrs := sqs.QueueConfigAttributes{
		DelaySeconds: "10",
		CustomAttributes: map[string]string{
			"test": "test",
		},
	}

	attrs, err := structAttrs.Attributes()
	require.NoError(t, err)

	require.Equal(
		t,
		map[string]string{
			"DelaySeconds": "10",
			"test":         "test",
		},
		attrs,
	)
}
