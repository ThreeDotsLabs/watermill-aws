package extendedclient

import (
	"fmt"
	"sync"
	"sync/atomic"

	snstypes "github.com/aws/aws-sdk-go-v2/service/sns/types"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
)

type TAttributeValueConstraint interface {
	sqstypes.MessageAttributeValue | snstypes.MessageAttributeValue
}

type ExtendedClient[TAttributeValue TAttributeValueConstraint] struct {
	S3c                       S3Client
	Logger                    Logger
	BucketName                string
	MessageSizeThreshold      int64
	BatchMessageSizeThreshold int64
	AlwaysThroughS3           bool
	NeverThroughS3            bool
	PointerClass              string
	ReservedAttrs             []string
	ObjectPrefix              string
	BaseS3PointerSize         int
	BaseAttributeSize         int
}

// S3Key returns a new string object key and prepends c.ObjectPrefix if it exists.
func (c *ExtendedClient[_]) S3Key(filename string) string {
	if c.ObjectPrefix != "" {
		return fmt.Sprintf("%s/%s", c.ObjectPrefix, filename)
	}
	return filename
}

// MessageSize returns the size of the body and attributes of a message
func (c *ExtendedClient[TAttributeValue]) MessageSize(body *string, attributes map[string]TAttributeValue) MessageSize {
	return MessageSize{
		BodySize:      int64(len(*body)),
		AttributeSize: c.AttributeSize(attributes),
	}
}

// MessageExceedsThreshold determines if the size of the body and attributes exceeds the configured
// message size threshold
func (c *ExtendedClient[TAttributeValue]) MessageExceedsThreshold(body *string, attributes map[string]TAttributeValue) bool {
	return c.MessageSize(body, attributes).Total() > c.MessageSizeThreshold
}

// AttributeSize will return the size of all provided attributes and their values
func (c *ExtendedClient[TAttributeValue]) AttributeSize(attributes map[string]TAttributeValue) int64 {
	sum := &atomic.Int64{}
	var wg sync.WaitGroup
	for k, v := range attributes {
		wg.Add(1)
		go func(k string, attr TAttributeValue) {
			var currentSum int64
			switch typedAttr := any(attr).(type) {
			case sqstypes.MessageAttributeValue:
				currentSum += int64(len([]byte(k)))
				currentSum += int64(len(typedAttr.BinaryValue))
				if typedAttr.StringValue != nil {
					currentSum += int64(len(*typedAttr.StringValue))
				}
				if typedAttr.DataType != nil {
					currentSum += int64(len(*typedAttr.DataType))
				}
			case snstypes.MessageAttributeValue:
				currentSum += int64(len([]byte(k)))
				currentSum += int64(len(typedAttr.BinaryValue))
				if typedAttr.StringValue != nil {
					currentSum += int64(len(*typedAttr.StringValue))
				}
				if typedAttr.DataType != nil {
					currentSum += int64(len(*typedAttr.DataType))
				}
			}
			sum.Add(currentSum)

			wg.Done()
		}(k, v)
	}
	wg.Wait()
	return sum.Load()
}

func (c *ExtendedClient[_]) OptimizeBatchPayload(bp *BatchPayload, messages []BatchMessageMeta) *BatchPayload {
	// return if we have no more messages to examine
	if len(messages) == 0 {
		return bp
	}

	currMsg := messages[0]
	numExtMsg := len(bp.ExtendedMessages)

	// case 1 - assume we leave the message as-is
	c1 := c.OptimizeBatchPayload(&BatchPayload{
		BatchBytes:       bp.BatchBytes + currMsg.MsgSize.Total(),
		ExtendedMessages: bp.ExtendedMessages,
		S3PointerSize:    bp.S3PointerSize,
	}, messages[1:])

	// case 2 - assume we convert the message into an extended payload
	extendedMessageSize := currMsg.MsgSize.ToExtendedSize(bp.S3PointerSize, c.BaseAttributeSize).Total()
	c2 := c.OptimizeBatchPayload(&BatchPayload{
		BatchBytes:       bp.BatchBytes + extendedMessageSize,
		ExtendedMessages: append(bp.ExtendedMessages[:numExtMsg:numExtMsg], currMsg),
		S3PointerSize:    bp.S3PointerSize,
	}, messages[1:])

	// preform the checks against factors provided in the function description
	if c1.BatchBytes <= c.BatchMessageSizeThreshold && c2.BatchBytes > c.BatchMessageSizeThreshold {
		return c1
	} else if c2.BatchBytes <= c.BatchMessageSizeThreshold && c1.BatchBytes > c.BatchMessageSizeThreshold {
		return c2
	} else if c1.BatchBytes > c.BatchMessageSizeThreshold && c2.BatchBytes > c.BatchMessageSizeThreshold {
		// in this case, both payloads suck- attempt to return the best of the worst
		if c1.BatchBytes <= c2.BatchBytes {
			return c1
		}
		return c2
	} else if len(c2.ExtendedMessages) > len(c1.ExtendedMessages) {
		return c1
	} else if c1.BatchBytes > c2.BatchBytes {
		return c1
	}

	return c2
}
