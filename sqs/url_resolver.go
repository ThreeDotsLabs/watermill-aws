package sqs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/ThreeDotsLabs/watermill"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
)

type GetQueueUrlByNameUrlResolverConfig struct {
	// DoNotCacheQueues disables caching of queue URLs.
	DoNotCacheQueues bool

	// GenerateGetQueueUrlInput generates *sqs.GetQueueUrlInput for AWS SDK.
	GenerateGetQueueUrlInput GenerateGetQueueUrlInputFunc
}

// GetQueueUrlByNameUrlResolver resolves queue url by calling AWS API.
// Topic name passed to Publisher.Publish, Subscriber.Subscribe is mapped to queue name.
//
// By default, queue URL is cached. It can be changed with GetQueueUrlByNameUrlResolverConfig.
type GetQueueUrlByNameUrlResolver struct {
	config GetQueueUrlByNameUrlResolverConfig

	queuesCache     map[string]QueueURL
	queuesCacheLock sync.RWMutex
}

func NewGetQueueUrlByNameUrlResolver(
	config GetQueueUrlByNameUrlResolverConfig,
) *GetQueueUrlByNameUrlResolver {
	if config.GenerateGetQueueUrlInput == nil {
		config.GenerateGetQueueUrlInput = GenerateGetQueueUrlInputDefault
	}

	return &GetQueueUrlByNameUrlResolver{
		config:      config,
		queuesCache: map[string]QueueURL{},
	}
}

func (p *GetQueueUrlByNameUrlResolver) ResolveQueueUrl(ctx context.Context, params ResolveQueueUrlParams) (res QueueUrlResolverResult, err error) {
	getQueueInput, err := p.config.GenerateGetQueueUrlInput(ctx, params.Topic)
	if err != nil {
		return QueueUrlResolverResult{}, fmt.Errorf("cannot generate input for queue %s: %w", params.Topic, err)
	}

	var getQueueInputHash string

	if !p.config.DoNotCacheQueues {
		getQueueInputHash = generateGetQueueUrlInputHash(getQueueInput)

		if getQueueInputHash != "" {
			p.queuesCacheLock.RLock()
			var ok bool
			queueUrl, ok := p.queuesCache[getQueueInputHash]
			p.queuesCacheLock.RUnlock()

			if ok {
				params.Logger.Trace("Used cache", watermill.LogFields{
					"topic": params.Topic,
					"queue": queueUrl,
					"hash":  getQueueInputHash,
				})

				// it can be present in cache only if it was created
				exists := true

				return QueueUrlResolverResult{
					QueueName: QueueName(params.Topic), // in this scenario topic maps to queue name
					QueueURL:  &queueUrl,
					Exists:    &exists,
				}, nil
			}
		}
	}

	defer func() {
		if err == nil && res.QueueURL != nil && *res.QueueURL != "" && getQueueInputHash != "" {
			p.queuesCacheLock.Lock()
			p.queuesCache[getQueueInputHash] = *res.QueueURL
			p.queuesCacheLock.Unlock()

			params.Logger.Trace("Stored cache", watermill.LogFields{
				"topic":     params.Topic,
				"queue_url": res.QueueURL,
				"hash":      getQueueInputHash,
			})
		}
	}()

	// it can be present in cache only if it was created
	queueUrl, err := getQueueUrl(ctx, params.SqsClient, params.Topic, getQueueInput)
	if err == nil {
		exists := true

		return QueueUrlResolverResult{
			QueueName: QueueName(params.Topic), // in this scenario topic maps to queue name
			QueueURL:  queueUrl,
			Exists:    &exists,
		}, nil
	}
	var queueDoesNotExistsErr *types.QueueDoesNotExist
	if errors.As(err, &queueDoesNotExistsErr) {
		exists := false

		return QueueUrlResolverResult{
			QueueName: QueueName(params.Topic), // in this scenario topic maps to queue name
			Exists:    &exists,
		}, nil
	}

	return QueueUrlResolverResult{}, err
}

func generateGetQueueUrlInputHash(getQueueInput *sqs.GetQueueUrlInput) string {
	// we are not using fmt.Sprintf because of pointers under the hood
	// we are not hashing specific struct fields to keep forward compatibility
	// also, json.Marshal is faster than fmt.Sprintf
	b, _ := json.Marshal(getQueueInput)
	return string(b)
}

type GenerateGetQueueUrlInputFunc func(ctx context.Context, topic string) (*sqs.GetQueueUrlInput, error)

func GenerateGetQueueUrlInputDefault(ctx context.Context, topic string) (*sqs.GetQueueUrlInput, error) {
	return &sqs.GetQueueUrlInput{
		QueueName: aws.String(topic),
	}, nil
}

// QueueUrlResolver resolves queue URL by topic passed to Publisher.Publish, Subscriber.Subscribe.
type QueueUrlResolver interface {
	ResolveQueueUrl(ctx context.Context, params ResolveQueueUrlParams) (QueueUrlResolverResult, error)
}

type ResolveQueueUrlParams struct {
	// Topic passed to Publisher.Publish, Subscriber.Subscribe, etc.
	// It may be mapped to a different name by QueueUrlResolver.
	Topic     string
	SqsClient *sqs.Client
	Logger    watermill.LoggerAdapter
}

type QueueUrlResolverResult struct {
	QueueName QueueName

	// QueueURL is not present if queue doesn't exist.
	QueueURL *QueueURL

	// Exists says if queue exists.
	// May be nil, if resolver doesn't have information about queue existence.
	Exists *bool
}

// GenerateQueueUrlResolver is a resolver that generates queue URL based on AWS region and account ID.
// Topic name passed to Publisher.Publish, Subscriber.Subscribe is mapped to queue name.
type GenerateQueueUrlResolver struct {
	AwsRegion    string
	AwsAccountID string
}

func (p GenerateQueueUrlResolver) ResolveQueueUrl(ctx context.Context, params ResolveQueueUrlParams) (QueueUrlResolverResult, error) {
	queueURL := QueueURL(fmt.Sprintf(
		"https://sqs.%s.amazonaws.com/%s/%s",
		p.AwsRegion, p.AwsAccountID, params.Topic,
	))

	return QueueUrlResolverResult{
		QueueName: QueueName(params.Topic), // in this case topic maps 1:1 to topic name
		QueueURL:  &queueURL,
		Exists:    nil, // we don't know
	}, nil
}

// TransparentUrlResolver is a resolver that uses topic passed to Publisher.Publish, Subscriber.Subscribe as queue URL.
// In this case, you should pass queue URL as topic.
type TransparentUrlResolver struct{}

func (p TransparentUrlResolver) ResolveQueueUrl(ctx context.Context, params ResolveQueueUrlParams) (res QueueUrlResolverResult, err error) {
	topicParts := strings.Split(params.Topic, "/")
	queueName := topicParts[len(topicParts)-1]

	queueURL := QueueURL(params.Topic)

	return QueueUrlResolverResult{
		QueueName: QueueName(queueName),
		QueueURL:  &queueURL, // in this case topic maps to queue URL
		Exists:    nil,       // we don't know
	}, nil
}
