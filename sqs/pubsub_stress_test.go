//go:build stress
// +build stress

package sqs

func TestPubSub_stress(t *testing.T) {
	tests.TestPubSubStressTest(
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
