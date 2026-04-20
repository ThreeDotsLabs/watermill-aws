package extendedclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"

	"github.com/aws/aws-sdk-go-v2/service/s3"
)

const (
	LegacyReservedAttributeName = "SQSLargePayloadSize"
	S3ObjectIDAtrributeName     = "S3ObjectKey"
)

var (
	ValidObjectNameRegex = regexp.MustCompile("^[0-9a-zA-Z!_.*'()-]+$")
	ErrObjectPrefix      = errors.New("object prefix contains invalid characters")
)

type (
	S3Client interface {
		PutObject(ctx context.Context, params *s3.PutObjectInput, optFns ...func(*s3.Options)) (*s3.PutObjectOutput, error)
		GetObject(ctx context.Context, params *s3.GetObjectInput, optFns ...func(*s3.Options)) (*s3.GetObjectOutput, error)
		DeleteObject(ctx context.Context, params *s3.DeleteObjectInput, optFns ...func(*s3.Options)) (*s3.DeleteObjectOutput, error)
		DeleteObjects(ctx context.Context, params *s3.DeleteObjectsInput, optFns ...func(*s3.Options)) (*s3.DeleteObjectsOutput, error)
	}
	Logger interface {
		Warn(msg string, args ...any)
	}
)

// MessageSize describes the size of a SQS message (body and its attributes)
type MessageSize struct {
	BodySize      int64
	AttributeSize int64
}

// Total returns the full message size
func (m MessageSize) Total() int64 {
	return m.BodySize + m.AttributeSize
}

// ToExtendedSize will convert a MessageSize to its equivalent extended payload size. This can be
// useful for estimating the size of a message if it were to be converted without actually having to
// handle the conversion.
func (m MessageSize) ToExtendedSize(pointerSize, attributeSize int) MessageSize {
	n, numDigits := int64(10), int64(1)
	for n <= m.BodySize {
		n *= 10
		numDigits++
	}

	return MessageSize{
		BodySize:      int64(pointerSize),
		AttributeSize: int64(attributeSize) + numDigits + m.AttributeSize,
	}
}

type S3Pointer struct {
	S3BucketName string
	S3Key        string
	Class        string
}

func (p *S3Pointer) UnmarshalJSON(in []byte) error {
	var ptr []interface{}

	if err := json.Unmarshal(in, &ptr); err != nil {
		return err
	}

	if len(ptr) != 2 {
		return fmt.Errorf("invalid pointer format, expected length 2, but received [%d]", len(ptr))
	}

	p.S3BucketName = ptr[1].(map[string]interface{})["s3BucketName"].(string)
	p.S3Key = ptr[1].(map[string]interface{})["s3Key"].(string)
	p.Class = ptr[0].(string)

	return nil
}

func (p *S3Pointer) MarshalJSON() ([]byte, error) {
	return []byte(fmt.Sprintf(`["%s",{"s3BucketName":"%s","s3Key":"%s"}]`, p.Class, p.S3BucketName, p.S3Key)), nil
}

// BatchMessageMeta is used to maintain a reference to the original payload inside of a batch
// request while also storing metadata about its size to be used during the optimize step
type BatchMessageMeta struct {
	PayloadIndex int
	MsgSize      MessageSize
}

// BatchPayload stores information about a combination of messages in order to determine the most
// efficient batches
type BatchPayload struct {
	BatchBytes       int64
	S3PointerSize    int
	ExtendedMessages []BatchMessageMeta
}
