package sns

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/aws/smithy-go/endpoints"
)

// OverrideEndpointResolver is a custom endpoint resolver that always returns the same endpoint.
// It can be used to use AWS emulators like Localstack.
//
// For example:
// import (
//
//	"github.com/ThreeDotsLabs/watermill-amazonsns/sns"
//	amazonsns "github.com/aws/aws-sdk-go-v2/service/sns"
//	"github.com/aws/smithy-go/transport"
//
// )
//
//	pub, err := sns.NewPublisher(sns.PublisherConfig{
//			AWSConfig: cfg,
//			Marshaler: sns.DefaultMarshalerUnmarshaler{},
//			SNSOptFns: []func(*amazonsns.Options){
//				amazonsns.WithEndpointResolverV2(sns.OverrideEndpointResolver{
//					Endpoint: transport.Endpoint{
//						URI: url.URL{Scheme: "http", Host: "localstack:4566"},
//					},
//				}),
//			},
//		}, logger)
type OverrideEndpointResolver struct {
	Endpoint transport.Endpoint
}

func (o OverrideEndpointResolver) ResolveEndpoint(ctx context.Context, params sns.EndpointParameters) (transport.Endpoint, error) {
	return o.Endpoint, nil
}
