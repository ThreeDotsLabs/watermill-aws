package sns

import (
	"unsafe"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/aws/aws-sdk-go-v2/service/sns/types"

	"github.com/Aryon-Security/watermill-aws/sqs"

	"github.com/ThreeDotsLabs/watermill/message"
)

type Marshaler interface {
	Marshal(topicArn TopicArn, msg *message.Message) *sns.PublishInput
}

type DefaultMarshalerUnmarshaler struct{}

func (d DefaultMarshalerUnmarshaler) Marshal(topicArn TopicArn, msg *message.Message) *sns.PublishInput {
	// client side uuid
	// there is a deduplication id that can be use for
	// fifo queues
	attributes, deduplicationId, groupId := metadataToAttributes(msg.Metadata)
	attributes[sqs.UUIDAttribute] = types.MessageAttributeValue{
		StringValue: aws.String(msg.UUID),
		DataType:    aws.String("String"),
	}

	// Create a string that shares the same underlying memory as the byte slice
	// This avoids making a copy of the data
	payloadStr := unsafe.String(unsafe.SliceData(msg.Payload), len(msg.Payload))
	return &sns.PublishInput{
		Message:                aws.String(payloadStr),
		MessageAttributes:      attributes,
		MessageDeduplicationId: deduplicationId,
		MessageGroupId:         groupId,
		TargetArn:              aws.String(string(topicArn)),
	}
}

func metadataToAttributes(meta message.Metadata) (map[string]types.MessageAttributeValue, *string, *string) {
	attributes := make(map[string]types.MessageAttributeValue)
	var deduplicationId, groupId *string
	for k, v := range meta {
		// SNS has special attributes for deduplication and group id
		if k == MessageDeduplicationIdMetadataField {
			deduplicationId = aws.String(v)
			continue
		}
		if k == MessageGroupIdMetadataField {
			groupId = aws.String(v)
			continue
		}
		attributes[k] = types.MessageAttributeValue{
			StringValue: aws.String(v),
			DataType:    aws.String("String"),
		}
	}

	return attributes, deduplicationId, groupId
}

const MessageDeduplicationIdMetadataField = "MessageDeduplicationId"

const MessageGroupIdMetadataField = "MessageGroupId"
