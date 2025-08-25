package sqs

import (
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"github.com/ThreeDotsLabs/watermill/message"
)

const UUIDAttribute = "_watermill_message_uuid"

type Marshaler interface {
	Marshal(msg *message.Message) (*types.Message, error)
}

type Unmarshaler interface {
	Unmarshal(msg *types.Message) (*message.Message, error)
}

type DefaultMarshalerUnmarshaler struct{}

func (d DefaultMarshalerUnmarshaler) Marshal(msg *message.Message) (*types.Message, error) {
	attributes := metadataToAttributes(msg.Metadata)
	// client side uuid
	// there is a deduplication id that can be use for
	// fifo queues
	attributes[UUIDAttribute] = types.MessageAttributeValue{
		StringValue: aws.String(msg.UUID),
		DataType:    aws.String(AWSStringDataType),
	}
	return &types.Message{
		MessageAttributes: attributes,
		Body:              aws.String(string(msg.Payload)),
	}, nil
}

func (d DefaultMarshalerUnmarshaler) Unmarshal(msg *types.Message) (*message.Message, error) {
	var uuid, payload string
	attributes := attributesToMetadata(msg.MessageAttributes)
	if value, ok := msg.MessageAttributes[UUIDAttribute]; ok {
		uuid = *value.StringValue
		delete(attributes, UUIDAttribute)
	} else if msg.MessageId != nil {
		// fallback when message is not published by Watermill
		uuid = *msg.MessageId
	}

	if msg.Body != nil {
		payload = *msg.Body
	}

	wmsg := message.NewMessage(uuid, []byte(payload))
	wmsg.Metadata = attributes

	return wmsg, nil
}

func metadataToAttributes(meta message.Metadata) map[string]types.MessageAttributeValue {
	attributes := make(map[string]types.MessageAttributeValue)

	for k, v := range meta {
		attributes[k] = types.MessageAttributeValue{
			StringValue: aws.String(v),
			DataType:    aws.String(AWSStringDataType),
		}
	}

	return attributes
}

func attributesToMetadata(attributes map[string]types.MessageAttributeValue) message.Metadata {
	meta := make(message.Metadata)

	for k, v := range attributes {
		if v.DataType == nil {
			continue
		}

		switch *v.DataType {
		case AWSStringDataType, AWSNumberDataType:
			if v.StringValue != nil {
				meta[k] = *v.StringValue
			}
		case AWSBinaryDataType:
			meta[k] = string(v.BinaryValue)
		}
	}

	return meta
}

const (
	AWSStringDataType = "String"
	AWSNumberDataType = "Number"
	AWSBinaryDataType = "Binary"
)
