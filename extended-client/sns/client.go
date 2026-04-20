package sns

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/aws/aws-sdk-go-v2/service/sns/types"
	"github.com/google/uuid"
	"golang.org/x/sync/errgroup"

	"github.com/Aryon-Security/watermill-aws/extended-client"
)

const MaxMsgSizeInBytes = 1 << 18 // 256 KiB (262,144 bytes)

var (
	jsonMarshal = json.Marshal
)

// Client is a wrapper for the [github.com/aws/aws-sdk-go-v2/service/sns.Client], providing extra
// functionality for retrieving, sending and deleting messages.
type Client struct {
	extendedclient.ExtendedClient[types.MessageAttributeValue]
	SNSClient
}

type ClientOption func(*Client) error

// New returns a newly created [*Client] with defaults:
//   - MessageSizeThreshold: 262144 (256 KiB)
//   - BatchMessageSizeThreshold: 262144 (256 KiB)
//   - S3PointerClass: "software.amazon.payloadoffloading.PayloadS3Pointer"
//   - ReservedAttributeName: "ExtendedPayloadSize"
//
// Further options can be passed in to configure these or other options. See [ClientOption]
// functions for more details.
func New(
	snsc SNSClient,
	s3c extendedclient.S3Client,
	optFns ...ClientOption,
) (*Client, error) {
	c := Client{
		SNSClient: snsc,
		ExtendedClient: extendedclient.ExtendedClient[types.MessageAttributeValue]{
			S3c:                       s3c,
			Logger:                    slog.New(slog.NewTextHandler(os.Stdout, nil)),
			MessageSizeThreshold:      MaxMsgSizeInBytes,
			BatchMessageSizeThreshold: MaxMsgSizeInBytes,
			PointerClass:              "software.amazon.payloadoffloading.PayloadS3Pointer",
			ReservedAttrs:             []string{"ExtendedPayloadSize", extendedclient.LegacyReservedAttributeName},
		},
	}

	// apply optFns to the base client
	for _, optFn := range optFns {
		err := optFn(&c)
		if err != nil {
			return nil, err
		}
	}

	// create an example s3 pointer
	ptr := &extendedclient.S3Pointer{
		S3Key: uuid.NewString(),
		Class: c.PointerClass,
	}

	// get its string representation
	s3PointerBytes, _ := ptr.MarshalJSON()

	// store the length of this string to be used when calculating optimal payload sizing in the
	// BatchSendMessage method. Note the size with the S3 Bucket is excluded here as this can
	// change on a per-call basis.
	c.BaseS3PointerSize = len(s3PointerBytes)

	// similarly, store the base size of the attribute added for extended payloads. Note the string
	// representation of the length of the payload is also omitted here as that obviously changes
	// between each message.
	c.BaseAttributeSize = len(c.ReservedAttrs[0]) + len("Number")

	return &c, nil
}

// WithLogger allows the caller to control how messages will be logged from the client. The expected
// interface matches the `log/slog` function signature and will default to a TextHandler unless
// overwritten by this method.
func WithLogger(logger extendedclient.Logger) ClientOption {
	return func(c *Client) error {
		c.Logger = logger
		return nil
	}
}

// Set the destination bucket for large messages that are sent by this client. This is a
// soft-requirement for using the SendMessage function.
func WithS3BucketName(bucketName string) ClientOption {
	return func(c *Client) error {
		c.BucketName = bucketName
		return nil
	}
}

// Set the MessageSizeThreshold to some other value (in bytes). By default this is 262144 (256
// KiB).
func WithMessageSizeThreshold(size int) ClientOption {
	return func(c *Client) error {
		c.MessageSizeThreshold = int64(size)
		return nil
	}
}

// Set the BatchMessageSizeThreshold to some other value (in bytes). By default this is 262144 (256
// KiB).
func WithBatchMessageSizeThreshold(size int) ClientOption {
	return func(c *Client) error {
		c.BatchMessageSizeThreshold = int64(size)
		return nil
	}
}

// Set the behavior of the client to always send messages to S3, regardless of the size of their
// body or attributes. By default this is false.
func WithAlwaysS3(alwaysS3 bool) ClientOption {
	return func(c *Client) error {
		c.AlwaysThroughS3 = alwaysS3
		return nil
	}
}

// Set the behavior of the client to never send messages to S3, regardless of the size of their
// body or attributes. By default this is false.
func WithNeverS3(neverS3 bool) ClientOption {
	return func(c *Client) error {
		c.NeverThroughS3 = neverS3
		return nil
	}
}

// WithReservedAttributeNames allows the user of the client to provide a list of attributes that
// will be used to identify large messages both sent and received by the created client. When
// sending messages, only the first attribute provided will be attached to the MessageAttributes.
// When receiving messages, all provided attributes will be checked to determine if the message has
// an extended payload in S3.
func WithReservedAttributeNames(attributeNames []string) ClientOption {
	return func(c *Client) error {
		c.ReservedAttrs = attributeNames
		return nil
	}
}

// Override PointerClass with custom value (i.e. [LegacyS3PointerClass])
func WithPointerClass(PointerClass string) ClientOption {
	return func(c *Client) error {
		c.PointerClass = PointerClass
		return nil
	}
}

// WithObjectPrefix attaches a prefix to the object key (prefix/uuid)
func WithObjectPrefix(prefix string) ClientOption {
	return func(c *Client) error {
		if !extendedclient.ValidObjectNameRegex.MatchString(prefix) {
			return extendedclient.ErrObjectPrefix
		}
		c.ObjectPrefix = prefix
		return nil
	}
}

func (c *Client) Publish(ctx context.Context, params *sns.PublishInput, optFns ...func(*sns.Options)) (*sns.PublishOutput, error) {
	// copy to avoid mutating params
	input := *params

	// determine bucket name, either from client (default) or from provided Topic ARN
	targetArn, s3Bucket, found := strings.Cut(*params.TargetArn, "|")
	if !found {
		s3Bucket = c.BucketName
	}

	input.TargetArn = &targetArn

	if s3Bucket != "" && !c.NeverThroughS3 && (c.AlwaysThroughS3 || c.MessageExceedsThreshold(input.Message, input.MessageAttributes)) {
		var s3RandomKey string
		if s3RandomKeyVal, found := input.MessageAttributes[extendedclient.S3ObjectIDAtrributeName]; found {
			s3RandomKey = *s3RandomKeyVal.StringValue
		} else {
			// generate s3 object key
			s3RandomKey = uuid.NewString()
		}
		s3Key := c.S3Key(s3RandomKey)

		// upload large payload to S3
		_, err := c.S3c.PutObject(ctx, &s3.PutObjectInput{
			Bucket: &s3Bucket,
			Key:    aws.String(s3Key),
			Body:   strings.NewReader(*input.Message),
		})

		if err != nil {
			return nil, fmt.Errorf("unable to upload large payload to s3: %w", err)
		}

		// create an s3 pointer that will be uploaded to SNS in place of the large payload
		asBytes, err := jsonMarshal(&extendedclient.S3Pointer{
			S3BucketName: s3Bucket,
			S3Key:        s3Key,
			Class:        c.PointerClass,
		})

		if err != nil {
			return nil, fmt.Errorf("unable to marshal S3 pointer: %w", err)
		}

		// copy over all attributes, leaving space for our reserved attribute
		updatedAttributes := make(map[string]types.MessageAttributeValue, len(input.MessageAttributes)+1)
		for k, v := range input.MessageAttributes {
			updatedAttributes[k] = v
		}

		// assign the reserved attribute to a number containing the size of the original body
		updatedAttributes[c.ReservedAttrs[0]] = types.MessageAttributeValue{
			DataType:    aws.String("Number"),
			StringValue: aws.String(strconv.Itoa(len(*input.Message))),
		}

		// override attributes and body in the original message
		input.MessageAttributes = updatedAttributes
		input.Message = aws.String(string(asBytes))
	} else {
		// delete s3 object key if it exists, because we dont need it as the message is small
		delete(input.MessageAttributes, extendedclient.S3ObjectIDAtrributeName)
	}

	return c.SNSClient.Publish(ctx, &input, optFns...)
}

func (c *Client) PublishBatch(ctx context.Context, params *sns.PublishBatchInput, optFns ...func(*sns.Options)) (*sns.PublishBatchOutput, error) {
	input := *params
	copyEntries := make([]types.PublishBatchRequestEntry, len(input.PublishBatchRequestEntries))

	// determine bucket name, either from client (default) or from provided Topic Arn
	topicArn, s3Bucket, found := strings.Cut(*params.TopicArn, "|")
	if !found {
		s3Bucket = c.BucketName
	}

	input.TopicArn = &topicArn

	// initialize the payload struct which will hold the data describing the batch
	bp := &extendedclient.BatchPayload{
		S3PointerSize:    c.BaseS3PointerSize + len(s3Bucket),
		ExtendedMessages: make([]extendedclient.BatchMessageMeta, 0, len(input.PublishBatchRequestEntries)),
	}

	// store "regular" (non-extended) messages separately to iterate during optimize step
	regularMessages := make([]extendedclient.BatchMessageMeta, 0, len(input.PublishBatchRequestEntries))
	for i, e := range input.PublishBatchRequestEntries {
		i, e := i, e

		// always copy the entry
		copyEntries[i] = e

		// calculate the starting message size
		msgSize := c.MessageSize(e.Message, e.MessageAttributes)

		// build a "meta" struct in order to keep track of this message when optimizing the batch
		// payload in the next stage
		msgMeta := extendedclient.BatchMessageMeta{
			PayloadIndex: i,
			MsgSize:      msgSize,
		}

		// check if we always send through s3, or if the message size exceeds the threshold
		if s3Bucket != "" && !c.NeverThroughS3 && (c.AlwaysThroughS3 || msgSize.Total() > c.MessageSizeThreshold) {
			// track the payload under the batch's ExtendedMessages
			bp.ExtendedMessages = append(bp.ExtendedMessages, msgMeta)

			// update the base payload size
			bp.BatchBytes += msgSize.ToExtendedSize(bp.S3PointerSize, c.BaseAttributeSize).Total()
		} else {
			// delete s3 object key if it exists, because we dont need it as the message is small
			delete(copyEntries[i].MessageAttributes, extendedclient.S3ObjectIDAtrributeName)
			regularMessages = append(regularMessages, msgMeta)
		}
	}

	// attempt to find the most efficient message combination for our batch
	bp = c.OptimizeBatchPayload(bp, regularMessages)

	if bp.BatchBytes > c.BatchMessageSizeThreshold {
		c.Logger.Warn(fmt.Sprintf("SendMessageBatch is only able to reduce the batch size to <%d> even though BatchMessageSizeThreshold is set to <%d>. Errors might occur.", bp.BatchBytes, c.BatchMessageSizeThreshold))
	}

	g := new(errgroup.Group)
	for _, em := range bp.ExtendedMessages {
		var s3RandomKey string
		if s3RandomKeyVal, found := copyEntries[em.PayloadIndex].MessageAttributes[extendedclient.S3ObjectIDAtrributeName]; found {
			s3RandomKey = *s3RandomKeyVal.StringValue
		} else {
			// generate s3 object key
			s3RandomKey = uuid.NewString()
		}
		// generate s3 object key
		s3Key := c.S3Key(s3RandomKey)

		// deep copy full message payload to send to S3
		msgBody := *copyEntries[em.PayloadIndex].Message

		// upload large payload to S3
		g.Go(func() error {
			_, err := c.S3c.PutObject(ctx, &s3.PutObjectInput{
				Bucket: &s3Bucket,
				Key:    aws.String(s3Key),
				Body:   strings.NewReader(msgBody),
			})

			if err != nil {
				return fmt.Errorf("unable to upload large payload to s3: %w", err)
			}

			return nil
		})

		// create an s3 pointer that will be published to SNS in place of the large payload
		asBytes, err := jsonMarshal(&extendedclient.S3Pointer{
			S3BucketName: s3Bucket,
			S3Key:        s3Key,
			Class:        c.PointerClass,
		})

		if err != nil {
			return nil, fmt.Errorf("unable to marshal S3 pointer: %w", err)
		}

		// copy over all attributes, leaving space for our reserved attribute
		updatedAttributes := make(map[string]types.MessageAttributeValue, len(copyEntries[em.PayloadIndex].MessageAttributes)+1)
		for k, v := range copyEntries[em.PayloadIndex].MessageAttributes {
			updatedAttributes[k] = v
		}

		// assign the reserved attribute to a number containing the size of the original body
		updatedAttributes[c.ReservedAttrs[0]] = types.MessageAttributeValue{
			DataType:    aws.String("Number"),
			StringValue: aws.String(strconv.FormatInt(em.MsgSize.BodySize, 10)),
		}

		// override attributes and body in the original message
		copyEntries[em.PayloadIndex].MessageAttributes = updatedAttributes
		copyEntries[em.PayloadIndex].Message = aws.String(string(asBytes))
	}

	if err := g.Wait(); err != nil {
		return nil, err
	}

	// override entries with our copied ones
	input.PublishBatchRequestEntries = copyEntries

	return c.SNSClient.PublishBatch(ctx, &input, optFns...)
}

func (c *Client) Subscribe(ctx context.Context, params *sns.SubscribeInput, optFns ...func(*sns.Options)) (*sns.SubscribeOutput, error) {
	input := *params

	// If the bucket seperator exists, we only need the actual arn, so we cut it out
	topicArn, _, _ := strings.Cut(*input.TopicArn, "|")
	input.TopicArn = &topicArn
	return c.SNSClient.Subscribe(ctx, &input, optFns...)
}
