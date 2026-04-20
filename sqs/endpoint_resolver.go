package sqs

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/smithy-go/endpoints"
)

// OverrideEndpointResolver is a custom endpoint resolver that always returns the same endpoint.
// It can be used to use AWS emulators like Localstack.
//
// For example:
// import (
//
//	"github.com/Aryon-Security/watermill-aws/sqs"
//	amazonsqs "github.com/aws/aws-sdk-go-v2/service/sqs"
//	"github.com/aws/smithy-go/transport"
//
// )
//
//	pub, err := sqs.NewPublisher(sqs.PublisherConfig{
//			AWSConfig: cfg,
//			Marshaler: sqs.DefaultMarshalerUnmarshaler{},
//			SNSOptFns: []func(*amazonsqs.Options){
//				amazonsqs.WithEndpointResolverV2(sqs.OverrideEndpointResolver{
//					Endpoint: transport.Endpoint{
//						URI: url.URL{Scheme: "http", Host: "localstack:4566"},
//					},
//				}),
//			},
//		}, logger)
type OverrideEndpointResolver struct {
	Endpoint transport.Endpoint
}

func (o OverrideEndpointResolver) ResolveEndpoint(context.Context, sqs.EndpointParameters) (transport.Endpoint, error) {
	return o.Endpoint, nil
}

type S3OverrideEndpointResolver struct {
	Endpoint transport.Endpoint
}

func (o S3OverrideEndpointResolver) ResolveEndpoint(context.Context, s3.EndpointParameters) (transport.Endpoint, error) {
	return o.Endpoint, nil
}
