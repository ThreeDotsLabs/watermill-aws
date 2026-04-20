package sqs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"github.com/Aryon-Security/watermill-aws/extended-client"
)

type mockSQSClient struct {
	*mock.Mock
	SQSClient
}

func (m *mockSQSClient) SendMessage(ctx context.Context, params *sqs.SendMessageInput, optFns ...func(*sqs.Options)) (*sqs.SendMessageOutput, error) {
	args := m.Called(ctx, params, optFns)
	return args.Get(0).(*sqs.SendMessageOutput), args.Error(1)
}

func (m *mockSQSClient) SendMessageBatch(ctx context.Context, params *sqs.SendMessageBatchInput, optFns ...func(*sqs.Options)) (*sqs.SendMessageBatchOutput, error) {
	args := m.Called(ctx, params, optFns)
	return args.Get(0).(*sqs.SendMessageBatchOutput), args.Error(1)
}

func (m *mockSQSClient) ReceiveMessage(ctx context.Context, params *sqs.ReceiveMessageInput, optFns ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error) {
	args := m.Called(ctx, params, optFns)
	return args.Get(0).(*sqs.ReceiveMessageOutput), args.Error(1)
}

func (m *mockSQSClient) DeleteMessage(ctx context.Context, params *sqs.DeleteMessageInput, optFns ...func(*sqs.Options)) (*sqs.DeleteMessageOutput, error) {
	args := m.Called(ctx, params, optFns)
	return args.Get(0).(*sqs.DeleteMessageOutput), args.Error(1)
}

func (m *mockSQSClient) DeleteMessageBatch(ctx context.Context, params *sqs.DeleteMessageBatchInput, optFns ...func(*sqs.Options)) (*sqs.DeleteMessageBatchOutput, error) {
	args := m.Called(ctx, params, optFns)
	return args.Get(0).(*sqs.DeleteMessageBatchOutput), args.Error(1)
}

func (m *mockSQSClient) ChangeMessageVisibility(ctx context.Context, params *sqs.ChangeMessageVisibilityInput, optFns ...func(*sqs.Options)) (*sqs.ChangeMessageVisibilityOutput, error) {
	args := m.Called(ctx, params, optFns)
	return args.Get(0).(*sqs.ChangeMessageVisibilityOutput), args.Error(1)
}

type mockS3Client struct {
	*mock.Mock
}

func (m *mockS3Client) PutObject(ctx context.Context, params *s3.PutObjectInput, optFns ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	args := m.Called(ctx, params, optFns)
	return args.Get(0).(*s3.PutObjectOutput), args.Error(1)
}

func (m *mockS3Client) GetObject(ctx context.Context, params *s3.GetObjectInput, optFns ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	args := m.Called(ctx, params, optFns)
	return args.Get(0).(*s3.GetObjectOutput), args.Error(1)
}

func (m *mockS3Client) DeleteObject(ctx context.Context, params *s3.DeleteObjectInput, optFns ...func(*s3.Options)) (*s3.DeleteObjectOutput, error) {
	args := m.Called(ctx, params, optFns)
	return args.Get(0).(*s3.DeleteObjectOutput), args.Error(1)
}

func (m *mockS3Client) DeleteObjects(ctx context.Context, params *s3.DeleteObjectsInput, optFns ...func(*s3.Options)) (*s3.DeleteObjectsOutput, error) {
	args := m.Called(ctx, params, optFns)
	return args.Get(0).(*s3.DeleteObjectsOutput), args.Error(1)
}

type errReader struct{}

func (errReader) Read(b []byte) (int, error) { return 0, errors.New("boom") }

func getDefaultS3Pointer(bucket, key string) string {
	return fmt.Sprintf(`["software.amazon.payloadoffloading.PayloadS3Pointer",{"s3BucketName":"%s","s3Key":"%s"}]`, bucket, key)
}

func TestNewClient(t *testing.T) {
	c, err := New(nil, nil)
	assert.Nil(t, err)

	// ensure defaults are set correctly
	assert.Equal(t, int64(262144), c.MessageSizeThreshold)
	assert.Equal(t, "software.amazon.payloadoffloading.PayloadS3Pointer", c.PointerClass)
	assert.Equal(t, []string{"ExtendedPayloadSize", "SQSLargePayloadSize"}, c.ReservedAttrs)
}

func TestNewClientOptions(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	c, err := New(
		nil,
		nil,
		WithAlwaysS3(true),
		WithMessageSizeThreshold(123),
		WithPointerClass("pointer.class"),
		WithReservedAttributeNames([]string{"Reserved", "Attributes"}),
		WithS3BucketName("BUCKET!"),
		WithObjectPrefix("custom_prefix"),
		WithLogger(logger),
	)

	assert.Nil(t, err)

	// ensure options are set correctly
	assert.Equal(t, true, c.AlwaysThroughS3)
	assert.Equal(t, int64(123), c.MessageSizeThreshold)
	assert.Equal(t, "pointer.class", c.PointerClass)
	assert.Equal(t, []string{"Reserved", "Attributes"}, c.ReservedAttrs)
	assert.Equal(t, "BUCKET!", c.BucketName)
	assert.Equal(t, "custom_prefix", c.ObjectPrefix)
	assert.Equal(t, logger, c.Logger)
}

func TestNewClientOptionsFailure(t *testing.T) {
	c, err := New(
		nil,
		nil,
		func(c *Client) error { return errors.New("boom") },
	)

	assert.ErrorContains(t, err, "boom")
	assert.Nil(t, c)
}

func TestNewClientSizeCalculation(t *testing.T) {
	testCases := []struct {
		desc                  string
		options               []ClientOption
		expectedAttributeSize int
		expectedPointerSize   int
	}{
		{
			desc:                  "default sizes",
			options:               nil,
			expectedAttributeSize: 25,
			expectedPointerSize:   121,
		},
		{
			desc:                  "custom pointer class",
			options:               []ClientOption{WithPointerClass("custom.pointer")},
			expectedAttributeSize: 25,
			expectedPointerSize:   85,
		},
		{
			desc:                  "custom reserved attributes",
			options:               []ClientOption{WithReservedAttributeNames([]string{"CustomAttr"})},
			expectedAttributeSize: 16,
			expectedPointerSize:   121,
		},
	}
	for _, tC := range testCases {
		t.Run(tC.desc, func(t *testing.T) {
			c, err := New(nil, nil, tC.options...)
			assert.NoError(t, err)
			assert.Equal(t, tC.expectedAttributeSize, c.BaseAttributeSize)
			assert.Equal(t, tC.expectedPointerSize, c.BaseS3PointerSize)
		})
	}

}

func TestAttributeSize(t *testing.T) {
	c, err := New(nil, nil)
	assert.Nil(t, err)

	assert.Equal(t, int64(26), c.AttributeSize(map[string]types.MessageAttributeValue{
		"testing_strings": {
			StringValue: aws.String("some string"),
		},
	}))

	assert.Equal(t, int64(20), c.AttributeSize(map[string]types.MessageAttributeValue{
		"testing_data_type": {
			DataType: aws.String("int"),
		},
	}))

	assert.Equal(t, int64(20), c.AttributeSize(map[string]types.MessageAttributeValue{
		"testing_binary": {
			BinaryValue: []byte{1, 2, 3, 4, 5, 6},
		},
	}))

	assert.Equal(t, int64(65), c.AttributeSize(map[string]types.MessageAttributeValue{
		"binary_attr": {
			BinaryValue: []byte{1, 2, 3, 4, 5, 6},
		},
		"string_attr1": {
			StringValue: aws.String("str"),
		},
		"string_attr2": {
			StringValue: aws.String("str"),
		},
		"data_type_attr1": {
			DataType: aws.String("int"),
		},
	}))
}

func TestMessageExceedThreshold(t *testing.T) {
	c, err := New(nil, nil, WithMessageSizeThreshold(10))
	assert.Nil(t, err)

	assert.False(t, c.MessageExceedsThreshold(
		aws.String("nnnnnnnnnn"),
		map[string]types.MessageAttributeValue{},
	))

	assert.True(t, c.MessageExceedsThreshold(
		aws.String("nnnnnnnnnnn"),
		map[string]types.MessageAttributeValue{},
	))

	assert.False(t, c.MessageExceedsThreshold(
		aws.String("nnnnn"),
		map[string]types.MessageAttributeValue{
			"str": {StringValue: aws.String("hi")},
		},
	))

	assert.True(t, c.MessageExceedsThreshold(
		aws.String("nnnnnn"),
		map[string]types.MessageAttributeValue{
			"str": {StringValue: aws.String("hi")},
		},
	))
}

func TestS3PointerMarshal(t *testing.T) {
	p := extendedclient.S3Pointer{
		S3BucketName: "some-bucket",
		S3Key:        "some-key",
		Class:        "com.james.testing.Pointer",
	}

	asBytes, err := p.MarshalJSON()
	assert.Nil(t, err)
	assert.Equal(t, `["com.james.testing.Pointer",{"s3BucketName":"some-bucket","s3Key":"some-key"}]`, string(asBytes))
}

func TestS3PointerUnmarshal(t *testing.T) {
	str := []byte(`["com.james.testing.Pointer",{"s3BucketName":"some-bucket","s3Key":"some-key"}]`)

	var p extendedclient.S3Pointer
	err := p.UnmarshalJSON(str)
	assert.Nil(t, err)
	assert.Equal(t, extendedclient.S3Pointer{
		S3BucketName: "some-bucket",
		S3Key:        "some-key",
		Class:        "com.james.testing.Pointer",
	}, p)
}

func TestS3PointerUnmarshalError(t *testing.T) {
	var p extendedclient.S3Pointer
	err := p.UnmarshalJSON([]byte(""))
	assert.NotNil(t, err)
}

func TestS3PointerUnmarshalInvalidLength(t *testing.T) {
	str := []byte(`["com.james.testing.Pointer",{"s3BucketName":"some-bucket","s3Key":"some-key"}, "bonus!"]`)

	var p extendedclient.S3Pointer
	err := p.UnmarshalJSON(str)
	assert.ErrorContains(t, err, "invalid pointer format, expected length 2, but received [3]")
}

func TestS3Key(t *testing.T) {
	uuid := uuid.New().String()
	tests := []struct {
		name          string
		prefix        string
		filename      string
		expectedS3Key string
	}{
		{"with prefix", "test", uuid, fmt.Sprintf("%s/%s", "test", uuid)},
		{"without prefix", "", uuid, uuid},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var c *Client
			var err error
			if test.prefix != "" {
				c, err = New(nil, nil, WithObjectPrefix(test.prefix))
			} else {
				c, err = New(nil, nil)
			}
			assert.Nil(t, err)
			s3Key := c.S3Key(test.filename)
			assert.Equal(t, test.expectedS3Key, s3Key)
		})
	}
}

func TestWithObjectPrefix(t *testing.T) {
	invalidPrefixes := []string{"../test", "./test", "tes&", "te$t", "testñ", "te@st", "test=", "test;", "test:", "+test", "te st", "te,st", "test?", "te\\st", "test{", "test^", "test}", "te`st", "]test", "test\"", "test>", "test]", "test~", "test<", "te#st", "|test"}
	validPrefixes := []string{"test0", "test", "TESt", "te!st", "te-st", "te_st", "te.st", "test*", "'test'", "(test)"}

	tests := []struct {
		name        string
		prefixes    []string
		expectedErr error
	}{
		{"invalid prefixes", invalidPrefixes, extendedclient.ErrObjectPrefix},
		{"valid prefixes", validPrefixes, nil},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			for _, prefix := range test.prefixes {
				c, err := New(
					nil,
					nil,
					WithObjectPrefix(prefix),
				)
				if test.expectedErr == nil {
					assert.Equal(t, c.ObjectPrefix, prefix)
				} else {
					assert.Equal(t, test.expectedErr, err)
				}
			}
		})
	}
}

func TestSendMessage(t *testing.T) {
	key := new(string)

	ms3c := &mockS3Client{&mock.Mock{}}
	ms3c.On(
		"PutObject",
		mock.Anything,
		mock.MatchedBy(func(params *s3.PutObjectInput) bool {
			key = params.Key

			assert.Greater(t, len(*params.Key), 0)
			assert.Equal(t, "override_bucket", *params.Bucket)

			asBytes, err := io.ReadAll(params.Body)
			assert.Nil(t, err)
			assert.Equal(t, "testing body", string(asBytes))

			return true
		}),
		mock.Anything).
		Return(&s3.PutObjectOutput{}, nil)

	msqsc := &mockSQSClient{Mock: &mock.Mock{}}
	msqsc.On(
		"SendMessage",
		mock.Anything,
		mock.MatchedBy(func(params *sqs.SendMessageInput) bool {
			assert.Equal(t, "12", *params.MessageAttributes["ExtendedPayloadSize"].StringValue)
			assert.Equal(t, "hi", *params.MessageAttributes["testing_attribute"].StringValue)
			assert.Equal(t, getDefaultS3Pointer("override_bucket", *key), *params.MessageBody)
			assert.Equal(t, "testing_url", *params.QueueUrl)

			return true
		}),
		mock.Anything).
		Return(&sqs.SendMessageOutput{}, nil)

	c, err := New(msqsc, ms3c, WithAlwaysS3(true), WithS3BucketName("test_bucket"))
	assert.Nil(t, err)

	_, err = c.SendMessage(context.Background(), &sqs.SendMessageInput{
		MessageBody: aws.String("testing body"),
		MessageAttributes: map[string]types.MessageAttributeValue{
			"testing_attribute": {StringValue: aws.String("hi")},
		},
		QueueUrl: aws.String("testing_url|override_bucket"),
	})

	assert.Nil(t, err)
}

func TestSendMessageS3Failure(t *testing.T) {
	ms3c := &mockS3Client{&mock.Mock{}}
	ms3c.On("PutObject", mock.Anything, mock.Anything, mock.Anything).Return(&s3.PutObjectOutput{}, errors.New("boom"))

	c, err := New(nil, ms3c, WithAlwaysS3(true), WithS3BucketName("test-bucket"))
	assert.Nil(t, err)

	_, err = c.SendMessage(
		context.Background(),
		&sqs.SendMessageInput{
			MessageBody: aws.String("testing body"),
			QueueUrl:    aws.String("testing_url"),
		})

	assert.ErrorContains(t, err, "unable to upload large payload to s3")
}

func TestSendMessageMarshalFailure(t *testing.T) {
	jsonMarshal = func(v any) ([]byte, error) { return nil, errors.New("boom") }
	defer func() { jsonMarshal = json.Marshal }()

	ms3c := &mockS3Client{&mock.Mock{}}
	ms3c.On("PutObject", mock.Anything, mock.Anything, mock.Anything).Return(&s3.PutObjectOutput{}, nil)

	c, err := New(nil, ms3c, WithAlwaysS3(true), WithS3BucketName("test-bucket"))
	assert.Nil(t, err)

	_, err = c.SendMessage(
		context.Background(),
		&sqs.SendMessageInput{
			MessageBody: aws.String("testing body"),
			QueueUrl:    aws.String("testing_url"),
		})

	assert.ErrorContains(t, err, "unable to marshal S3 pointer")
}

func TestOptimizeBatchPayload(t *testing.T) {
	testCases := []struct {
		desc             string
		clientOptions    []ClientOption
		baseBatchPayload extendedclient.BatchPayload
		messages         []extendedclient.BatchMessageMeta
		checks           func(*testing.T, *extendedclient.BatchPayload)
	}{
		{
			desc:          "payload under threshold",
			clientOptions: []ClientOption{WithBatchMessageSizeThreshold(50)},
			messages: []extendedclient.BatchMessageMeta{
				{PayloadIndex: 0, MsgSize: extendedclient.MessageSize{BodySize: 10}},
				{PayloadIndex: 1, MsgSize: extendedclient.MessageSize{BodySize: 30}},
				{PayloadIndex: 2, MsgSize: extendedclient.MessageSize{BodySize: 5}},
			},
			baseBatchPayload: extendedclient.BatchPayload{},
			checks: func(t *testing.T, bp *extendedclient.BatchPayload) {
				assert.Equal(t, int64(10+30+5), bp.BatchBytes)
				assert.Len(t, bp.ExtendedMessages, 0)
			},
		},
		{
			desc:          "small payloads under large threshold",
			clientOptions: []ClientOption{WithBatchMessageSizeThreshold(5000)},
			messages: []extendedclient.BatchMessageMeta{
				{PayloadIndex: 0, MsgSize: extendedclient.MessageSize{BodySize: 10}},
				{PayloadIndex: 1, MsgSize: extendedclient.MessageSize{BodySize: 30}},
				{PayloadIndex: 2, MsgSize: extendedclient.MessageSize{BodySize: 5}},
			},
			baseBatchPayload: extendedclient.BatchPayload{},
			checks: func(t *testing.T, bp *extendedclient.BatchPayload) {
				assert.Equal(t, int64(10+30+5), bp.BatchBytes)
				assert.Len(t, bp.ExtendedMessages, 0)
			},
		},
		{
			desc:          "payload equals threshold",
			clientOptions: []ClientOption{WithBatchMessageSizeThreshold(45)},
			messages: []extendedclient.BatchMessageMeta{
				{PayloadIndex: 0, MsgSize: extendedclient.MessageSize{BodySize: 10}},
				{PayloadIndex: 1, MsgSize: extendedclient.MessageSize{BodySize: 30}},
				{PayloadIndex: 2, MsgSize: extendedclient.MessageSize{BodySize: 5}},
			},
			baseBatchPayload: extendedclient.BatchPayload{},
			checks: func(t *testing.T, bp *extendedclient.BatchPayload) {
				assert.Equal(t, int64(10+30+5), bp.BatchBytes)
				assert.Len(t, bp.ExtendedMessages, 0)
			},
		},
		{
			desc:          "single message causes payload to exceed threshold",
			clientOptions: []ClientOption{WithBatchMessageSizeThreshold(200)},
			messages: []extendedclient.BatchMessageMeta{
				{PayloadIndex: 1, MsgSize: extendedclient.MessageSize{BodySize: 30}},
				{PayloadIndex: 2, MsgSize: extendedclient.MessageSize{BodySize: 5}},
				{PayloadIndex: 3, MsgSize: extendedclient.MessageSize{BodySize: 200}}, // replaced with a payload of size 149
			},
			baseBatchPayload: extendedclient.BatchPayload{},
			checks: func(t *testing.T, bp *extendedclient.BatchPayload) {
				assert.Equal(t, int64(30+5+149), bp.BatchBytes)
				assert.Len(t, bp.ExtendedMessages, 1)
				assert.Equal(t, 3, bp.ExtendedMessages[0].PayloadIndex)
			},
		},
		{
			desc:          "all messages cause payload to exceed threshold",
			clientOptions: []ClientOption{WithBatchMessageSizeThreshold(300)},
			messages: []extendedclient.BatchMessageMeta{
				{PayloadIndex: 1, MsgSize: extendedclient.MessageSize{BodySize: 1000}},
				{PayloadIndex: 2, MsgSize: extendedclient.MessageSize{BodySize: 100}},
				{PayloadIndex: 3, MsgSize: extendedclient.MessageSize{BodySize: 120}},
				{PayloadIndex: 4, MsgSize: extendedclient.MessageSize{BodySize: 200}},
			},
			baseBatchPayload: extendedclient.BatchPayload{},
			checks: func(t *testing.T, bp *extendedclient.BatchPayload) {
				assert.Equal(t, int64(150+100+120+149), bp.BatchBytes)
				assert.Len(t, bp.ExtendedMessages, 2)
				assert.Equal(t, 1, bp.ExtendedMessages[0].PayloadIndex)
				assert.Equal(t, 4, bp.ExtendedMessages[1].PayloadIndex)
			},
		},
		{
			desc:          "minimize data sent to s3",
			clientOptions: []ClientOption{WithBatchMessageSizeThreshold(600)},
			messages: []extendedclient.BatchMessageMeta{
				{PayloadIndex: 1, MsgSize: extendedclient.MessageSize{BodySize: 400}},
				{PayloadIndex: 2, MsgSize: extendedclient.MessageSize{BodySize: 450}},
			},
			baseBatchPayload: extendedclient.BatchPayload{},
			checks: func(t *testing.T, bp *extendedclient.BatchPayload) {
				assert.Equal(t, int64(149+450), bp.BatchBytes)
				assert.Len(t, bp.ExtendedMessages, 1)
				assert.Equal(t, 1, bp.ExtendedMessages[0].PayloadIndex)
			},
		},
		{
			desc:          "minimize data sent to s3 - alternate",
			clientOptions: []ClientOption{WithBatchMessageSizeThreshold(800)},
			messages: []extendedclient.BatchMessageMeta{
				{PayloadIndex: 2, MsgSize: extendedclient.MessageSize{BodySize: 450}},
				{PayloadIndex: 1, MsgSize: extendedclient.MessageSize{BodySize: 400}},
			},
			baseBatchPayload: extendedclient.BatchPayload{},
			checks: func(t *testing.T, bp *extendedclient.BatchPayload) {
				assert.Equal(t, int64(149+450), bp.BatchBytes)
				assert.Len(t, bp.ExtendedMessages, 1)
				assert.Equal(t, 1, bp.ExtendedMessages[0].PayloadIndex)
			},
		},
	}
	for _, tC := range testCases {
		t.Run(tC.desc, func(t *testing.T) {
			c, err := New(nil, nil, tC.clientOptions...)
			assert.NoError(t, err)

			tC.baseBatchPayload.S3PointerSize += c.BaseS3PointerSize

			bp := c.OptimizeBatchPayload(&tC.baseBatchPayload, tC.messages)
			tC.checks(t, bp)
		})
	}
}

func TestSendMessageBatch(t *testing.T) {
	key1, key2 := new(string), new(string)
	ms3c := &mockS3Client{&mock.Mock{}}
	ms3c.On(
		"PutObject",
		mock.Anything,
		mock.MatchedBy(func(params *s3.PutObjectInput) bool {
			assert.Equal(t, "override_bucket", *params.Bucket)

			asBytes, err := io.ReadAll(params.Body)
			assert.Nil(t, err)

			switch string(asBytes) {
			case "testing body 1":
				key1 = params.Key
				return true
			case "testing body 2":
				key2 = params.Key
				return true
			}

			return false
		}),
		mock.Anything).
		Return(&s3.PutObjectOutput{}, nil)

	msqsc := &mockSQSClient{Mock: &mock.Mock{}}
	msqsc.On(
		"SendMessageBatch",
		mock.Anything,
		mock.MatchedBy(func(params *sqs.SendMessageBatchInput) bool {
			assert.Equal(t, "testing_url", *params.QueueUrl)
			assert.Len(t, params.Entries, 2)
			assert.Equal(t, "entry_1", *params.Entries[0].Id)
			assert.Equal(t, "entry_2", *params.Entries[1].Id)
			assert.Equal(t, getDefaultS3Pointer("override_bucket", *key1), *params.Entries[0].MessageBody)
			assert.Equal(t, getDefaultS3Pointer("override_bucket", *key2), *params.Entries[1].MessageBody)
			assert.Equal(t, "hi", *params.Entries[0].MessageAttributes["testing_attribute"].StringValue)
			assert.Equal(t, "hello", *params.Entries[1].MessageAttributes["testing_attribute"].StringValue)
			assert.Equal(t, "14", *params.Entries[0].MessageAttributes["ExtendedPayloadSize"].StringValue)
			assert.Equal(t, "14", *params.Entries[1].MessageAttributes["ExtendedPayloadSize"].StringValue)
			return true
		}),
		mock.Anything).
		Return(&sqs.SendMessageBatchOutput{}, nil)

	c, err := New(msqsc, ms3c, WithAlwaysS3(true), WithS3BucketName("test_bucket"))
	assert.Nil(t, err)

	_, err = c.SendMessageBatch(context.Background(), &sqs.SendMessageBatchInput{
		Entries: []types.SendMessageBatchRequestEntry{
			{
				Id:          aws.String("entry_1"),
				MessageBody: aws.String("testing body 1"),
				MessageAttributes: map[string]types.MessageAttributeValue{
					"testing_attribute": {StringValue: aws.String("hi")},
				},
			},
			{
				Id:          aws.String("entry_2"),
				MessageBody: aws.String("testing body 2"),
				MessageAttributes: map[string]types.MessageAttributeValue{
					"testing_attribute": {StringValue: aws.String("hello")},
				},
			},
		},
		QueueUrl: aws.String("testing_url|override_bucket"),
	})

	assert.Len(t, ms3c.Calls, 2)
	assert.Nil(t, err)
}

func TestSendMessageBatchMessageSizesBelowThreshold(t *testing.T) {
	ms3c := &mockS3Client{&mock.Mock{}}
	msqsc := &mockSQSClient{Mock: &mock.Mock{}}
	msqsc.On(
		"SendMessageBatch",
		mock.Anything,
		mock.MatchedBy(func(params *sqs.SendMessageBatchInput) bool {
			assert.Equal(t, "testing_url", *params.QueueUrl)
			assert.Len(t, params.Entries, 2)
			assert.Equal(t, "entry_1", *params.Entries[0].Id)
			assert.Equal(t, "entry_2", *params.Entries[1].Id)
			assert.Equal(t, "testing body 1", *params.Entries[0].MessageBody)
			assert.Equal(t, "testing body 2", *params.Entries[1].MessageBody)
			assert.Equal(t, "hi", *params.Entries[0].MessageAttributes["testing_attribute"].StringValue)
			assert.Equal(t, "hello", *params.Entries[1].MessageAttributes["testing_attribute"].StringValue)
			assert.Nil(t, params.Entries[0].MessageAttributes["ExtendedPayloadSize"].StringValue)
			assert.Nil(t, params.Entries[1].MessageAttributes["ExtendedPayloadSize"].StringValue)
			return true
		}),
		mock.Anything).
		Return(&sqs.SendMessageBatchOutput{}, nil)

	c, err := New(msqsc, ms3c, WithMessageSizeThreshold(500), WithS3BucketName("test_bucket"))
	assert.Nil(t, err)

	_, err = c.SendMessageBatch(context.Background(), &sqs.SendMessageBatchInput{
		Entries: []types.SendMessageBatchRequestEntry{
			{
				Id:          aws.String("entry_1"),
				MessageBody: aws.String("testing body 1"),
				MessageAttributes: map[string]types.MessageAttributeValue{
					"testing_attribute": {StringValue: aws.String("hi")},
				},
			},
			{
				Id:          aws.String("entry_2"),
				MessageBody: aws.String("testing body 2"),
				MessageAttributes: map[string]types.MessageAttributeValue{
					"testing_attribute": {StringValue: aws.String("hello")},
				},
			},
		},
		QueueUrl: aws.String("testing_url|override_bucket"),
	})

	assert.Zero(t, ms3c.Calls)
	assert.Len(t, msqsc.Calls, 1)
	assert.Nil(t, err)
}

func TestSendMessageBatchSizeAboveThreshold(t *testing.T) {
	ms3c := &mockS3Client{&mock.Mock{}}
	ms3c.On("PutObject", mock.Anything, mock.Anything, mock.Anything).Return(&s3.PutObjectOutput{}, nil)

	msqsc := &mockSQSClient{Mock: &mock.Mock{}}
	msqsc.On(
		"SendMessageBatch",
		mock.Anything,
		mock.MatchedBy(func(params *sqs.SendMessageBatchInput) bool {
			assert.Len(t, params.Entries, 3)
			assert.Equal(t, "entry_1", *params.Entries[0].Id)
			assert.Equal(t, "entry_2", *params.Entries[1].Id)
			assert.Equal(t, "entry_3", *params.Entries[2].Id)
			assert.Nil(t, params.Entries[0].MessageAttributes["ExtendedPayloadSize"].StringValue)
			assert.Nil(t, params.Entries[1].MessageAttributes["ExtendedPayloadSize"].StringValue)
			assert.Equal(t, "500", *params.Entries[2].MessageAttributes["ExtendedPayloadSize"].StringValue)
			assert.Equal(t, "testing body 1", *params.Entries[0].MessageBody)
			assert.Equal(t, "testing body 2 with a little larger payload", *params.Entries[1].MessageBody)
			return true
		}),
		mock.Anything).
		Return(&sqs.SendMessageBatchOutput{}, nil)

	c, err := New(msqsc, ms3c, WithBatchMessageSizeThreshold(20))
	assert.Nil(t, err)

	_, err = c.SendMessageBatch(context.Background(), &sqs.SendMessageBatchInput{
		Entries: []types.SendMessageBatchRequestEntry{
			{
				Id:          aws.String("entry_1"),
				MessageBody: aws.String("testing body 1"),
			},
			{
				Id:          aws.String("entry_2"),
				MessageBody: aws.String("testing body 2 with a little larger payload"),
			},
			{
				Id:          aws.String("entry_3"),
				MessageBody: aws.String(strings.Repeat("large", 100)),
			},
		},
		QueueUrl: aws.String("some_url"),
	})

	assert.Len(t, ms3c.Calls, 1)
	assert.Nil(t, err)
}

func TestSendMessageBatchFailure(t *testing.T) {
	ms3c := &mockS3Client{&mock.Mock{}}
	ms3c.On("PutObject", mock.Anything, mock.Anything, mock.Anything).Return(&s3.PutObjectOutput{}, errors.New("boom"))

	c, err := New(nil, ms3c, WithAlwaysS3(true), WithS3BucketName("test-bucket"))
	assert.Nil(t, err)

	_, err = c.SendMessageBatch(context.Background(), &sqs.SendMessageBatchInput{
		Entries: []types.SendMessageBatchRequestEntry{
			{MessageBody: aws.String("testing body 1")},
			{MessageBody: aws.String("testing body 2")},
		},
		QueueUrl: aws.String("testing_url"),
	})

	assert.ErrorContains(t, err, "unable to upload large payload to s3")
}

func TestSendMessageBatchMarshalFailure(t *testing.T) {
	jsonMarshal = func(v any) ([]byte, error) { return nil, errors.New("boom") }
	defer func() { jsonMarshal = json.Marshal }()

	ms3c := &mockS3Client{&mock.Mock{}}
	ms3c.On("PutObject", mock.Anything, mock.Anything, mock.Anything).Return(&s3.PutObjectOutput{}, nil)

	c, err := New(nil, ms3c, WithAlwaysS3(true), WithS3BucketName("test-bucket"))
	assert.Nil(t, err)

	_, err = c.SendMessageBatch(context.Background(), &sqs.SendMessageBatchInput{
		Entries: []types.SendMessageBatchRequestEntry{
			{MessageBody: aws.String("testing body 1")},
			{MessageBody: aws.String("testing body 2")},
		},
		QueueUrl: aws.String("testing_url"),
	})

	assert.ErrorContains(t, err, "unable to marshal S3 pointer")
}

func TestReceiveMessage(t *testing.T) {
	msqsc := &mockSQSClient{Mock: &mock.Mock{}}
	msqsc.On(
		"ReceiveMessage",
		mock.Anything,
		mock.MatchedBy(func(params *sqs.ReceiveMessageInput) bool {
			assert.Equal(t, []string{"CustomAttr", "ExtendedPayloadSize", "SQSLargePayloadSize"}, params.MessageAttributeNames)
			return true
		}),
		mock.Anything).
		Return(&sqs.ReceiveMessageOutput{Messages: []types.Message{
			{
				Body:              aws.String(getDefaultS3Pointer("test-bucket", "test-item")),
				MessageAttributes: map[string]types.MessageAttributeValue{"ExtendedPayloadSize": {}},
				ReceiptHandle:     aws.String("mock-handle-123"),
			},
			{
				Body:          aws.String("non s3 pointer body"),
				ReceiptHandle: aws.String("mock-handle-456"),
			},
			{
				Body:              aws.String(getDefaultS3Pointer("test-bucket-2", "test-item-2")),
				MessageAttributes: map[string]types.MessageAttributeValue{"SQSLargePayloadSize": {}},
				ReceiptHandle:     aws.String("mock-handle-123"),
			},
		}}, nil)

	ms3c := &mockS3Client{&mock.Mock{}}
	ms3c.On(
		"GetObject",
		mock.Anything,
		mock.MatchedBy(func(params *s3.GetObjectInput) bool {
			if *params.Bucket == "test-bucket" && *params.Key == "test-item" {
				return true
			}

			return false
		}),
		mock.Anything).
		Return(&s3.GetObjectOutput{Body: io.NopCloser(strings.NewReader("object1"))}, nil)

	ms3c.On(
		"GetObject",
		mock.Anything,
		mock.MatchedBy(func(params *s3.GetObjectInput) bool {
			if *params.Bucket == "test-bucket-2" && *params.Key == "test-item-2" {
				return true
			}

			return false
		}),
		mock.Anything).
		Return(&s3.GetObjectOutput{Body: io.NopCloser(strings.NewReader("object2"))}, nil)

	c, err := New(msqsc, ms3c)
	assert.Nil(t, err)

	resp, err := c.ReceiveMessage(context.Background(), &sqs.ReceiveMessageInput{
		MessageAttributeNames: []string{"CustomAttr"},
	})
	assert.Nil(t, err)

	assert.Equal(t, "object1", *resp.Messages[0].Body)
	assert.Equal(t, "-..s3BucketName..-test-bucket-..s3BucketName..--..s3Key..-test-item-..s3Key..-mock-handle-123", *resp.Messages[0].ReceiptHandle)
	assert.Equal(t, "non s3 pointer body", *resp.Messages[1].Body)
	assert.Equal(t, "mock-handle-456", *resp.Messages[1].ReceiptHandle)
	assert.Equal(t, "object2", *resp.Messages[2].Body)
	assert.Equal(t, "-..s3BucketName..-test-bucket-2-..s3BucketName..--..s3Key..-test-item-2-..s3Key..-mock-handle-123", *resp.Messages[2].ReceiptHandle)
}

func TestReceiveMessageAllAttributes(t *testing.T) {
	msqsc := &mockSQSClient{Mock: &mock.Mock{}}
	msqsc.On(
		"ReceiveMessage",
		mock.Anything,
		mock.MatchedBy(func(params *sqs.ReceiveMessageInput) bool {
			assert.Equal(t, []string{"All"}, params.MessageAttributeNames)
			return true
		}),
		mock.Anything).
		Return(&sqs.ReceiveMessageOutput{}, nil)

	c, err := New(msqsc, nil)
	assert.Nil(t, err)

	_, err = c.ReceiveMessage(context.Background(), &sqs.ReceiveMessageInput{
		MessageAttributeNames: []string{"All"},
	})
	assert.Nil(t, err)
}

func TestReceiveMessageAllAttributesAlternateSyntax(t *testing.T) {
	msqsc := &mockSQSClient{Mock: &mock.Mock{}}
	msqsc.On(
		"ReceiveMessage",
		mock.Anything,
		mock.MatchedBy(func(params *sqs.ReceiveMessageInput) bool {
			assert.Equal(t, []string{".*"}, params.MessageAttributeNames)
			return true
		}),
		mock.Anything).
		Return(&sqs.ReceiveMessageOutput{}, nil)

	c, err := New(msqsc, nil)
	assert.Nil(t, err)

	_, err = c.ReceiveMessage(context.Background(), &sqs.ReceiveMessageInput{
		MessageAttributeNames: []string{".*"},
	})
	assert.Nil(t, err)
}

func TestReceiveMessageNoDuplicateAttribute(t *testing.T) {
	msqsc := &mockSQSClient{Mock: &mock.Mock{}}
	msqsc.On(
		"ReceiveMessage",
		mock.Anything,
		mock.MatchedBy(func(params *sqs.ReceiveMessageInput) bool {
			assert.Equal(t, []string{"ExtendedPayloadSize", "something_else"}, params.MessageAttributeNames)
			return true
		}),
		mock.Anything).
		Return(&sqs.ReceiveMessageOutput{}, nil)

	c, err := New(msqsc, nil)
	assert.Nil(t, err)

	_, err = c.ReceiveMessage(context.Background(), &sqs.ReceiveMessageInput{
		MessageAttributeNames: []string{"ExtendedPayloadSize", "something_else"},
	})
	assert.Nil(t, err)
}

func TestReceiveMessageError(t *testing.T) {
	msqsc := &mockSQSClient{Mock: &mock.Mock{}}
	msqsc.On("ReceiveMessage", mock.Anything, mock.Anything, mock.Anything).Return(&sqs.ReceiveMessageOutput{}, errors.New("boom"))

	c, err := New(msqsc, nil)
	assert.Nil(t, err)

	_, err = c.ReceiveMessage(context.Background(), &sqs.ReceiveMessageInput{})
	assert.NotNil(t, err)
}

func TestReceiveMessageJSONError(t *testing.T) {
	jsonUnmarshal = func(data []byte, v any) error { return errors.New("boom") }
	defer func() { jsonUnmarshal = json.Unmarshal }()

	msqsc := &mockSQSClient{Mock: &mock.Mock{}}
	msqsc.
		On("ReceiveMessage", mock.Anything, mock.Anything, mock.Anything).
		Return(&sqs.ReceiveMessageOutput{Messages: []types.Message{
			{
				Body:              aws.String(getDefaultS3Pointer("test-bucket", "test-item")),
				MessageAttributes: map[string]types.MessageAttributeValue{"ExtendedPayloadSize": {}},
			},
		}}, nil)

	c, err := New(msqsc, nil)
	assert.Nil(t, err)

	_, err = c.ReceiveMessage(context.Background(), &sqs.ReceiveMessageInput{})
	assert.ErrorContains(t, err, "error when unmarshalling s3 pointer")
}

func TestReceiveMessageS3Error(t *testing.T) {
	msqsc := &mockSQSClient{Mock: &mock.Mock{}}
	msqsc.
		On("ReceiveMessage", mock.Anything, mock.Anything, mock.Anything).
		Return(&sqs.ReceiveMessageOutput{Messages: []types.Message{
			{
				Body:              aws.String(getDefaultS3Pointer("test-bucket", "test-item")),
				MessageAttributes: map[string]types.MessageAttributeValue{"ExtendedPayloadSize": {}},
				ReceiptHandle:     aws.String("mock-handle-123"),
			},
		}}, nil)

	ms3c := &mockS3Client{&mock.Mock{}}
	ms3c.On("GetObject", mock.Anything, mock.Anything, mock.Anything).Return(&s3.GetObjectOutput{}, errors.New("boom"))

	c, err := New(msqsc, ms3c)
	assert.Nil(t, err)

	_, err = c.ReceiveMessage(context.Background(), &sqs.ReceiveMessageInput{})
	assert.ErrorContains(t, err, "error when reading from s3 (test-bucket/test-item)")
}

func TestReceiveMessageReadError(t *testing.T) {
	msqsc := &mockSQSClient{Mock: &mock.Mock{}}
	msqsc.
		On("ReceiveMessage", mock.Anything, mock.Anything, mock.Anything).
		Return(&sqs.ReceiveMessageOutput{Messages: []types.Message{
			{
				Body:              aws.String(getDefaultS3Pointer("test-bucket", "test-item")),
				MessageAttributes: map[string]types.MessageAttributeValue{"ExtendedPayloadSize": {}},
				ReceiptHandle:     aws.String("mock-handle-123"),
			},
		}}, nil)

	ms3c := &mockS3Client{&mock.Mock{}}
	ms3c.
		On("GetObject", mock.Anything, mock.Anything, mock.Anything).
		Return(&s3.GetObjectOutput{Body: io.NopCloser(errReader{})}, nil)

	c, err := New(msqsc, ms3c)
	assert.Nil(t, err)

	_, err = c.ReceiveMessage(context.Background(), &sqs.ReceiveMessageInput{})
	assert.ErrorContains(t, err, "error when reading buffer")
}

func TestRetrieveLambdaEvent(t *testing.T) {
	ms3c := &mockS3Client{&mock.Mock{}}
	ms3c.
		On(
			"GetObject",
			mock.Anything,
			mock.MatchedBy(func(params *s3.GetObjectInput) bool {
				if *params.Bucket == "test-bucket" && *params.Key == "test-event" {
					return true
				}

				return false
			}),
			mock.Anything).
		Return(&s3.GetObjectOutput{Body: io.NopCloser(strings.NewReader("object1"))}, nil)

	ms3c.
		On(
			"GetObject",
			mock.Anything,
			mock.MatchedBy(func(params *s3.GetObjectInput) bool {
				if *params.Bucket == "test-bucket-2" && *params.Key == "test-event-2" {
					return true
				}

				return false
			}),
			mock.Anything).
		Return(&s3.GetObjectOutput{Body: io.NopCloser(strings.NewReader("object2"))}, nil)

	c, err := New(nil, ms3c)
	assert.Nil(t, err)

	resp, err := c.RetrieveLambdaEvent(context.Background(), &events.SQSEvent{
		Records: []events.SQSMessage{
			{
				Body:              getDefaultS3Pointer("test-bucket", "test-event"),
				MessageAttributes: map[string]events.SQSMessageAttribute{"ExtendedPayloadSize": {}},
				ReceiptHandle:     "something-or-other",
			},
			{
				Body:          "normal non-pointer body",
				ReceiptHandle: "something-else",
			},
			{
				Body:              getDefaultS3Pointer("test-bucket-2", "test-event-2"),
				MessageAttributes: map[string]events.SQSMessageAttribute{"SQSLargePayloadSize": {}},
				ReceiptHandle:     "mock-handle-123",
			},
		},
	})

	assert.Nil(t, err)
	assert.Equal(t, "object1", resp.Records[0].Body)
	assert.Equal(t, "-..s3BucketName..-test-bucket-..s3BucketName..--..s3Key..-test-event-..s3Key..-something-or-other", resp.Records[0].ReceiptHandle)
	assert.Equal(t, "normal non-pointer body", resp.Records[1].Body)
	assert.Equal(t, "something-else", resp.Records[1].ReceiptHandle)
	assert.Equal(t, "object2", resp.Records[2].Body)
	assert.Equal(t, "-..s3BucketName..-test-bucket-2-..s3BucketName..--..s3Key..-test-event-2-..s3Key..-mock-handle-123", resp.Records[2].ReceiptHandle)
}

func TestRetrieveLambdaEventJSONError(t *testing.T) {
	jsonUnmarshal = func(data []byte, v any) error { return errors.New("boom") }
	defer func() { jsonUnmarshal = json.Unmarshal }()

	c, err := New(nil, nil)
	assert.Nil(t, err)

	_, err = c.RetrieveLambdaEvent(context.Background(), &events.SQSEvent{
		Records: []events.SQSMessage{{
			Body:              getDefaultS3Pointer("test-bucket", "test-event"),
			MessageAttributes: map[string]events.SQSMessageAttribute{"ExtendedPayloadSize": {}},
		}},
	})

	assert.ErrorContains(t, err, "error when unmarshalling s3 pointer")
}

func TestRetrieveLambdaEventS3Error(t *testing.T) {
	ms3c := &mockS3Client{&mock.Mock{}}
	ms3c.
		On("GetObject", mock.Anything, mock.Anything, mock.Anything).
		Return(&s3.GetObjectOutput{}, errors.New("boom"))

	c, err := New(nil, ms3c)
	assert.Nil(t, err)

	_, err = c.RetrieveLambdaEvent(context.Background(), &events.SQSEvent{
		Records: []events.SQSMessage{{
			Body:              getDefaultS3Pointer("test-bucket", "test-event"),
			MessageAttributes: map[string]events.SQSMessageAttribute{"ExtendedPayloadSize": {}},
		}},
	})

	assert.ErrorContains(t, err, "error when reading from s3 (test-bucket/test-event)")
}

func TestRetrieveLambdaEventBufferError(t *testing.T) {
	ms3c := &mockS3Client{&mock.Mock{}}
	ms3c.
		On("GetObject", mock.Anything, mock.Anything, mock.Anything).
		Return(&s3.GetObjectOutput{Body: io.NopCloser(errReader{})}, nil)

	c, err := New(nil, ms3c)
	assert.Nil(t, err)

	_, err = c.RetrieveLambdaEvent(context.Background(), &events.SQSEvent{
		Records: []events.SQSMessage{{
			Body:              getDefaultS3Pointer("test-bucket", "test-event"),
			MessageAttributes: map[string]events.SQSMessageAttribute{"ExtendedPayloadSize": {}},
		}},
	})

	assert.ErrorContains(t, err, "error when reading buffer")
}

func TestParseExtendedReceiptHandle(t *testing.T) {
	bucket, key, handle := parseExtendedReceiptHandle("-..s3BucketName..-some-bucket-..s3BucketName..--..s3Key..-some-key-..s3Key..-abcdefg")
	assert.Equal(t, "some-bucket", bucket)
	assert.Equal(t, "some-key", key)
	assert.Equal(t, "abcdefg", handle)
}

func TestParseExtendedReceiptHandleFailure(t *testing.T) {
	bucket, key, handle := parseExtendedReceiptHandle("nonExtendedHandle")
	assert.Equal(t, "", bucket)
	assert.Equal(t, "", key)
	assert.Equal(t, "", handle)
}

func TestDeleteMessage(t *testing.T) {
	ms3c := &mockS3Client{&mock.Mock{}}
	ms3c.On(
		"DeleteObject",
		mock.Anything,
		mock.MatchedBy(func(params *s3.DeleteObjectInput) bool {
			assert.Equal(t, "some-bucket", *params.Bucket)
			assert.Equal(t, "some-key", *params.Key)
			return true
		}),
		mock.Anything).
		Return(&s3.DeleteObjectOutput{}, nil)

	msqsc := &mockSQSClient{Mock: &mock.Mock{}}
	msqsc.On(
		"DeleteMessage",
		mock.Anything,
		mock.MatchedBy(func(params *sqs.DeleteMessageInput) bool {
			assert.Equal(t, "abcdefg", *params.ReceiptHandle)
			return true
		}),
		mock.Anything).
		Return(&sqs.DeleteMessageOutput{}, nil)

	c, err := New(msqsc, ms3c)
	assert.Nil(t, err)

	_, err = c.DeleteMessage(context.Background(), &sqs.DeleteMessageInput{
		ReceiptHandle: aws.String("-..s3BucketName..-some-bucket-..s3BucketName..--..s3Key..-some-key-..s3Key..-abcdefg"),
	})

	assert.Nil(t, err)
}

func TestDeleteMessageNonExtended(t *testing.T) {
	msqsc := &mockSQSClient{Mock: &mock.Mock{}}
	msqsc.On(
		"DeleteMessage",
		mock.Anything,
		mock.MatchedBy(func(params *sqs.DeleteMessageInput) bool {
			assert.Equal(t, "non extended receipt handle", *params.ReceiptHandle)
			return true
		}),
		mock.Anything).
		Return(&sqs.DeleteMessageOutput{}, nil)

	c, err := New(msqsc, nil)
	assert.Nil(t, err)

	_, err = c.DeleteMessage(context.Background(), &sqs.DeleteMessageInput{
		ReceiptHandle: aws.String("non extended receipt handle"),
	})

	assert.Nil(t, err)
}

func TestDeleteMessageS3Error(t *testing.T) {
	msqsc := &mockSQSClient{Mock: &mock.Mock{}}
	msqsc.On(
		"DeleteMessage",
		mock.Anything,
		mock.MatchedBy(func(params *sqs.DeleteMessageInput) bool {
			assert.Equal(t, "abcdefg", *params.ReceiptHandle)
			return true
		}),
		mock.Anything).
		Return(&sqs.DeleteMessageOutput{}, nil)

	ms3c := &mockS3Client{&mock.Mock{}}
	ms3c.On("DeleteObject", mock.Anything, mock.Anything, mock.Anything).Return(&s3.DeleteObjectOutput{}, errors.New("boom"))

	c, err := New(msqsc, ms3c)
	assert.Nil(t, err)

	_, err = c.DeleteMessage(context.Background(), &sqs.DeleteMessageInput{
		ReceiptHandle: aws.String("-..s3BucketName..-some-bucket-..s3BucketName..--..s3Key..-some-key-..s3Key..-abcdefg"),
	})

	assert.Error(t, err)
}

func TestDeleteMessageSQSError(t *testing.T) {
	msqsc := &mockSQSClient{Mock: &mock.Mock{}}
	msqsc.On("DeleteMessage", mock.Anything, mock.Anything, mock.Anything).Return(&sqs.DeleteMessageOutput{}, errors.New("boom"))

	c, err := New(msqsc, nil)
	assert.Nil(t, err)

	_, err = c.DeleteMessage(context.Background(), &sqs.DeleteMessageInput{
		ReceiptHandle: aws.String("-..s3BucketName..-some-bucket-..s3BucketName..--..s3Key..-some-key-..s3Key..-abcdefg"),
	})

	assert.Error(t, err)
}

func TestDeleteMessageBatch(t *testing.T) {
	ms3c := &mockS3Client{&mock.Mock{}}
	ms3c.On(
		"DeleteObjects",
		mock.Anything,
		mock.MatchedBy(func(params *s3.DeleteObjectsInput) bool {
			switch *params.Bucket {
			case "some-bucket":
				assert.Equal(
					t,
					[]s3types.ObjectIdentifier{{Key: aws.String("some-key")}, {Key: aws.String("another-key")}},
					params.Delete.Objects,
				)
				return true
			case "alternate-bucket":
				assert.Equal(
					t,
					[]s3types.ObjectIdentifier{{Key: aws.String("yet-another-key")}},
					params.Delete.Objects,
				)
				return true
			default:
				return false
			}
		}),
		mock.Anything).
		Return(&s3.DeleteObjectsOutput{}, nil)

	msqsc := &mockSQSClient{Mock: &mock.Mock{}}
	msqsc.On(
		"DeleteMessageBatch",
		mock.Anything,
		mock.MatchedBy(func(params *sqs.DeleteMessageBatchInput) bool {
			assert.Len(t, params.Entries, 3)
			assert.Equal(t, "object_1", *params.Entries[0].Id)
			assert.Equal(t, "object_2", *params.Entries[1].Id)
			assert.Equal(t, "object_3", *params.Entries[2].Id)
			assert.Equal(t, "abcdefg", *params.Entries[0].ReceiptHandle)
			assert.Equal(t, "hijklmn", *params.Entries[1].ReceiptHandle)
			assert.Equal(t, "123", *params.Entries[2].ReceiptHandle)
			return true
		}),
		mock.Anything).
		Return(&sqs.DeleteMessageBatchOutput{}, nil)

	c, err := New(msqsc, ms3c)
	assert.Nil(t, err)

	_, err = c.DeleteMessageBatch(context.Background(), &sqs.DeleteMessageBatchInput{
		Entries: []types.DeleteMessageBatchRequestEntry{
			{
				Id:            aws.String("object_1"),
				ReceiptHandle: aws.String("-..s3BucketName..-some-bucket-..s3BucketName..--..s3Key..-some-key-..s3Key..-abcdefg"),
			},
			{
				Id:            aws.String("object_2"),
				ReceiptHandle: aws.String("-..s3BucketName..-some-bucket-..s3BucketName..--..s3Key..-another-key-..s3Key..-hijklmn"),
			},
			{
				Id:            aws.String("object_3"),
				ReceiptHandle: aws.String("-..s3BucketName..-alternate-bucket-..s3BucketName..--..s3Key..-yet-another-key-..s3Key..-123"),
			},
		},
	})

	assert.Nil(t, err)
}

func TestDeleteMessageBatchS3Error(t *testing.T) {
	ms3c := &mockS3Client{&mock.Mock{}}
	ms3c.On("DeleteObjects", mock.Anything, mock.Anything, mock.Anything).Return(&s3.DeleteObjectsOutput{}, errors.New("boom"))

	msqsc := &mockSQSClient{Mock: &mock.Mock{}}
	msqsc.On("DeleteMessageBatch", mock.Anything, mock.Anything, mock.Anything).Return(&sqs.DeleteMessageBatchOutput{}, nil)

	c, err := New(msqsc, ms3c)
	assert.Nil(t, err)

	_, err = c.DeleteMessageBatch(context.Background(), &sqs.DeleteMessageBatchInput{
		Entries: []types.DeleteMessageBatchRequestEntry{
			{
				Id:            aws.String("object_1"),
				ReceiptHandle: aws.String("-..s3BucketName..-some-bucket-..s3BucketName..--..s3Key..-some-key-..s3Key..-abcdefg"),
			},
		},
	})

	assert.Error(t, err)
}

func TestDeleteMessageBatchSQSError(t *testing.T) {
	msqsc := &mockSQSClient{Mock: &mock.Mock{}}
	msqsc.On("DeleteMessageBatch", mock.Anything, mock.Anything, mock.Anything).Return(&sqs.DeleteMessageBatchOutput{}, errors.New("boom"))

	c, err := New(msqsc, nil)
	assert.Nil(t, err)

	_, err = c.DeleteMessageBatch(context.Background(), &sqs.DeleteMessageBatchInput{
		Entries: []types.DeleteMessageBatchRequestEntry{
			{
				Id:            aws.String("object_1"),
				ReceiptHandle: aws.String("-..s3BucketName..-some-bucket-..s3BucketName..--..s3Key..-some-key-..s3Key..-abcdefg"),
			},
		},
	})

	assert.Error(t, err)
}

func TestClient_ChangeMessageVisibility(t *testing.T) {
	msqsc := &mockSQSClient{Mock: &mock.Mock{}}
	matcher := func(params *sqs.ChangeMessageVisibilityInput) bool {
		return *params.ReceiptHandle == "abcdefg"
	}
	msqsc.On("ChangeMessageVisibility", mock.Anything, mock.MatchedBy(matcher), mock.Anything).Return(&sqs.ChangeMessageVisibilityOutput{}, nil)

	c, err := New(msqsc, nil)
	assert.Nil(t, err)

	_, err = c.ChangeMessageVisibility(context.Background(), &sqs.ChangeMessageVisibilityInput{
		QueueUrl:          aws.String("test_queue"),
		ReceiptHandle:     aws.String("-..s3BucketName..-some-bucket-..s3BucketName..--..s3Key..-some-key-..s3Key..-abcdefg"),
		VisibilityTimeout: 10,
	})
	assert.NoError(t, err)
}
