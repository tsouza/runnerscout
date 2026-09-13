package provider

import (
	"context"
	"errors"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

// AWSSDK binds each operation's account observation and EC2 calls to exactly
// the same credentials. It never lets a later credential refresh change that
// binding between observing ownership and changing a resource.
type AWSSDK struct {
	scope      *awsCredentialScope
	HTTPClient aws.HTTPClient // Internal test injection; never product configuration.
	Endpoint   string
}

func (*AWSSDK) String() string   { return "AWS SDK (credentials redacted)" }
func (*AWSSDK) GoString() string { return "AWS SDK (credentials redacted)" }

func (s *AWSSDK) session(ctx context.Context, config Config, region string) (*ec2.Client, error) {
	if s == nil || s.scope == nil || region == "" {
		return nil, errors.New("AWS SDK configuration unavailable")
	}
	scope := *s.scope
	if s.HTTPClient != nil {
		scope.client = s.HTTPClient
	}
	if s.Endpoint != "" {
		scope.endpoint = s.Endpoint
	}
	value, err := scope.retrieve(ctx, region)
	if err != nil {
		return nil, err
	}
	clientConfig := scope.config(region, frozenAWSCredentials(value))
	who, err := scope.sts(clientConfig).GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil || who == nil {
		return nil, errors.New("AWS account identity unavailable")
	}
	if aws.ToString(who.Account) != config.AccountID || len(config.AccountID) != 12 {
		return nil, errors.New("AWS account identity mismatch")
	}
	clientConfig.HTTPClient = &awsEC2HTTPClient{base: clientConfig.HTTPClient}
	return ec2.NewFromConfig(clientConfig, func(options *ec2.Options) {
		if scope.endpoint != "" {
			options.BaseEndpoint = aws.String(scope.endpoint)
		}
	}), nil
}
