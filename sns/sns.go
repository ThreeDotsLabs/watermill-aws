package sns

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/service/sns"

	snsextendedclient "github.com/Aryon-Security/watermill-aws/extended-client/sns"
)

// TopicName is a name of the SNS topic
type TopicName string

// TopicArn is an ARN of the SNS topic
type TopicArn string

func createSnsTopic(ctx context.Context, snsClient snsextendedclient.SNSClient, createSNSParams sns.CreateTopicInput) (*string, error) {
	createSNSOutput, err := snsClient.CreateTopic(ctx, &createSNSParams)
	if err != nil || createSNSOutput.TopicArn == nil {
		return nil, fmt.Errorf("cannot create SNS topic %s: %w", *createSNSParams.Name, err)
	}
	return createSNSOutput.TopicArn, nil
}

// GenerateTopicArn generates an ARN for the SNS topic based on the region, accountID and topic name.
func GenerateTopicArn(region, accountID, topic string) (TopicArn, error) {
	var err error
	if region == "" {
		err = errors.Join(err, fmt.Errorf("region is empty"))
	}
	if accountID == "" {
		err = errors.Join(err, fmt.Errorf("accountID is empty"))
	}
	if topic == "" {
		err = errors.Join(err, fmt.Errorf("topic is empty"))
	}
	if err != nil {
		return "", fmt.Errorf("can't generate topic arn: %w", err)
	}

	return TopicArn(fmt.Sprintf("arn:aws:sns:%s:%s:%s", region, accountID, topic)), nil
}

// ExtractTopicNameFromTopicArn extracts the topic name from the topic ARN.
func ExtractTopicNameFromTopicArn(topicArn TopicArn) (TopicName, error) {
	topicArnParts := strings.Split(string(topicArn), ":")
	if len(topicArnParts) != 6 {
		return "", fmt.Errorf("topic arn should have 6 segments, has %d (%s)", len(topicArnParts), topicArn)
	}

	topicName := topicArnParts[5]
	return TopicName(topicName), nil
}
