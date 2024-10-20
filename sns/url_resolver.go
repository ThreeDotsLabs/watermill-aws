package sns

import (
	"context"
	"errors"
	"fmt"
)

// TopicResolver resolves topic name to topic ARN by topic passed to Publisher.Publish, Subscriber.Subscribe.
type TopicResolver interface {
	ResolveTopic(ctx context.Context, topic string) (snsTopic TopicArn, err error)
}

// TransparentTopicResolver is a TopicResolver that passes the topic as is.
// You should use SNS topic ARN as in Subscribe and Publish.
type TransparentTopicResolver struct{}

func (a TransparentTopicResolver) ResolveTopic(ctx context.Context, topic string) (snsTopic TopicArn, err error) {
	// we are passing topic ARN as topic
	return TopicArn(topic), nil
}

// GenerateArnTopicResolver is a TopicResolver that generates ARN for the topic
// using the provided account ID and region.
//
// You should use this resolver if you want to pass only the topic name to Subscribe and Publish.
type GenerateArnTopicResolver struct {
	accountID string
	region    string
}

func NewGenerateArnTopicResolver(accountID string, region string) (*GenerateArnTopicResolver, error) {
	var err error

	if accountID == "" {
		err = errors.Join(err, fmt.Errorf("accountID is empty"))
	}
	if region == "" {
		err = errors.Join(err, fmt.Errorf("region is empty"))
	}

	if err != nil {
		return nil, fmt.Errorf("failed to create GenerateArnTopicResolver: %w", err)
	}

	return &GenerateArnTopicResolver{accountID: accountID, region: region}, nil
}

func (g GenerateArnTopicResolver) ResolveTopic(ctx context.Context, topic string) (snsTopic TopicArn, err error) {
	return GenerateTopicArn(g.region, g.accountID, topic)
}
