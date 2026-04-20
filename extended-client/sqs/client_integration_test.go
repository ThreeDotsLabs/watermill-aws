package sqs

import (
	"context"
	"encoding/json"
	"io"
	"math/rand"
	"os"
	"strconv"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/stretchr/testify/assert"

	"github.com/Aryon-Security/watermill-aws/extended-client"
)

type Integration struct {
	t          *testing.T
	sqsec      *Client
	sqsc       *sqs.Client
	s3c        *s3.Client
	queueURL   string
	bucketName string
}

func startIntegrationTest(t *testing.T) *Integration {
	region := os.Getenv("AWS_REGION")
	bucketName := os.Getenv("BUCKET_NAME")
	queueURL := os.Getenv("QUEUE_URL")

	if testing.Short() {
		t.Skip("skipping integration test")
		return nil
	}

	if region == "" || bucketName == "" || queueURL == "" {
		t.Skip("missing one or more env vars: AWS_REGION, BUCKET_NAME, QUEUE_URL. skipping integration test.")
		return nil
	}

	ctx := context.Background()
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region))
	assert.NoError(t, err)

	sqsc := sqs.NewFromConfig(cfg)
	s3c := s3.NewFromConfig(cfg)
	sqsec, err := New(sqsc, s3c, WithS3BucketName(bucketName))
	assert.NoError(t, err)

	return &Integration{
		t:          t,
		sqsec:      sqsec,
		sqsc:       sqsc,
		s3c:        s3c,
		queueURL:   queueURL,
		bucketName: bucketName,
	}
}

var letters = []byte("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ")

func createPayload(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = letters[rand.Intn(len(letters))]
	}
	return string(b)
}

func createMaxPayload() string {
	return createPayload(MaxMsgSizeInBytes)
}

func createExtendedPayload() string {
	return createPayload(MaxMsgSizeInBytes + 1)
}

func TestBaselineExtendedPayloadIntegration(t *testing.T) {
	i := startIntegrationTest(t)

	_, err := i.sqsc.SendMessage(context.Background(), &sqs.SendMessageInput{
		MessageBody: aws.String(createExtendedPayload()),
		QueueUrl:    aws.String(i.queueURL),
	})

	// ensure that oversized payloads will fail to be sent to sqs
	assert.ErrorContains(t, err, "Message must be shorter than 262144 bytes")
}

func TestCoreFunctionalityIntegration(t *testing.T) {
	i := startIntegrationTest(t)
	payloads := []string{createMaxPayload(), createExtendedPayload(), createExtendedPayload()}

	tests := []struct {
		name                string
		payload             string
		receiveMessageFn    func(ctx context.Context, params *sqs.ReceiveMessageInput, optFns ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error)
		msgChecks           func(t *testing.T, msg types.Message)
		receiptHandleChecks func(t *testing.T, handle string)
	}{
		{
			name:             "non-extended payloads stay within queue",
			payload:          payloads[0],
			receiveMessageFn: i.sqsc.ReceiveMessage,
			msgChecks: func(t *testing.T, msg types.Message) {
				assert.NotContains(t, msg.MessageAttributes, "ExtendedPayloadSize")
				assert.Equal(t, payloads[0], *msg.Body)
			},
		},
		{
			name:             "extended payload goes to s3",
			payload:          payloads[1],
			receiveMessageFn: i.sqsc.ReceiveMessage,
			msgChecks: func(t *testing.T, msg types.Message) {
				assert.Contains(t, msg.MessageAttributes, "ExtendedPayloadSize")
				assert.Equal(t, strconv.Itoa(MaxMsgSizeInBytes+1), *msg.MessageAttributes["ExtendedPayloadSize"].StringValue)

				var extendedPointer extendedclient.S3Pointer
				err := json.Unmarshal([]byte(*msg.Body), &extendedPointer)
				assert.NoError(t, err)
				assert.Equal(t, "software.amazon.payloadoffloading.PayloadS3Pointer", extendedPointer.Class)
				assert.Equal(t, i.bucketName, extendedPointer.S3BucketName)

				s3r, err := i.s3c.GetObject(context.TODO(), &s3.GetObjectInput{
					Bucket: &extendedPointer.S3BucketName,
					Key:    &extendedPointer.S3Key,
				})
				assert.NoError(t, err)

				defer func() {
					_ = s3r.Body.Close()
				}()

				content, err := io.ReadAll(s3r.Body)
				assert.NoError(t, err)
				assert.Equal(t, payloads[1], string(content))

				_, err = i.s3c.DeleteObject(context.TODO(), &s3.DeleteObjectInput{
					Bucket: &extendedPointer.S3BucketName,
					Key:    &extendedPointer.S3Key,
				})
				assert.NoError(t, err)
			},
		},
		{
			name:             "extended payload extracted automatically using client",
			payload:          payloads[2],
			receiveMessageFn: i.sqsec.ReceiveMessage,
			msgChecks: func(t *testing.T, msg types.Message) {
				assert.Contains(t, msg.MessageAttributes, "ExtendedPayloadSize")
				assert.Equal(t, strconv.Itoa(MaxMsgSizeInBytes+1), *msg.MessageAttributes["ExtendedPayloadSize"].StringValue)
				assert.Equal(t, payloads[2], *msg.Body)

				bucket, key, handle := parseExtendedReceiptHandle(*msg.ReceiptHandle)
				assert.NotEmpty(t, bucket)
				assert.NotEmpty(t, key)
				assert.NotEmpty(t, handle)
			},
			receiptHandleChecks: func(t *testing.T, handle string) {
				bucket, key, _ := parseExtendedReceiptHandle(handle)

				_, err := i.s3c.GetObject(context.TODO(), &s3.GetObjectInput{
					Bucket: &bucket,
					Key:    &key,
				})

				assert.ErrorContains(t, err, "NoSuchKey")
			},
		},
	}

	for _, tC := range tests {
		t.Run(tC.name, func(t *testing.T) {
			_, err := i.sqsec.SendMessage(context.TODO(), &sqs.SendMessageInput{
				MessageBody: &tC.payload,
				QueueUrl:    aws.String(i.queueURL),
			})

			assert.NoError(t, err)

			res, err := tC.receiveMessageFn(context.TODO(), &sqs.ReceiveMessageInput{
				QueueUrl:              aws.String(i.queueURL),
				MessageAttributeNames: []string{"All"},
			})

			assert.NoError(t, err)

			msg := res.Messages[0]
			tC.msgChecks(t, msg)

			_, err = i.sqsec.DeleteMessage(context.TODO(), &sqs.DeleteMessageInput{
				QueueUrl:      aws.String(i.queueURL),
				ReceiptHandle: msg.ReceiptHandle,
			})

			assert.NoError(t, err)

			if tC.receiptHandleChecks != nil {
				tC.receiptHandleChecks(t, *msg.ReceiptHandle)
			}
		})
	}
}

func TestBatchFunctionality(t *testing.T) {
	i := startIntegrationTest(t)

	payloads := []string{createPayload(50), createExtendedPayload()}
	res, err := i.sqsec.SendMessageBatch(context.TODO(), &sqs.SendMessageBatchInput{
		Entries: []types.SendMessageBatchRequestEntry{
			{
				Id:          aws.String("standard"),
				MessageBody: aws.String(payloads[0]),
			},
			{
				Id:          aws.String("extended"),
				MessageBody: aws.String(payloads[1]),
			},
		},
		QueueUrl: aws.String(i.queueURL),
	})

	assert.NoError(t, err)
	assert.Len(t, res.Successful, 2)

	recResp, err := i.sqsec.ReceiveMessage(context.TODO(), &sqs.ReceiveMessageInput{
		QueueUrl:            aws.String(i.queueURL),
		AttributeNames:      []types.QueueAttributeName{},
		MaxNumberOfMessages: 2,
	})

	assert.NoError(t, err)
	assert.Len(t, recResp.Messages, 2)

	var bucket, key, handle string
	hasExtended := false
	hasStandard := false
	for _, msg := range recResp.Messages {
		if attr, ok := msg.MessageAttributes["ExtendedPayloadSize"]; ok {
			hasExtended = true
			assert.Equal(t, strconv.Itoa(MaxMsgSizeInBytes+1), *attr.StringValue)
			assert.Equal(t, payloads[1], *msg.Body)

			bucket, key, handle = parseExtendedReceiptHandle(*msg.ReceiptHandle)
			assert.NotEmpty(t, bucket)
			assert.NotEmpty(t, key)
			assert.NotEmpty(t, handle)
		} else {
			hasStandard = true
			assert.Equal(t, payloads[0], *msg.Body)
		}
	}

	assert.True(t, hasExtended)
	assert.True(t, hasStandard)

	assert.NoError(t, err)

	_, err = i.sqsec.DeleteMessageBatch(context.TODO(), &sqs.DeleteMessageBatchInput{
		Entries: []types.DeleteMessageBatchRequestEntry{
			{
				Id:            aws.String("1"),
				ReceiptHandle: recResp.Messages[0].ReceiptHandle,
			},
			{
				Id:            aws.String("2"),
				ReceiptHandle: recResp.Messages[1].ReceiptHandle,
			},
		},
		QueueUrl: aws.String(i.queueURL),
	})
	assert.NoError(t, err)

	_, err = i.s3c.GetObject(context.TODO(), &s3.GetObjectInput{
		Bucket: &bucket,
		Key:    &key,
	})

	assert.ErrorContains(t, err, "NoSuchKey")
}
