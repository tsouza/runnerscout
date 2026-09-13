package provider

import (
	"context"
	"errors"
	"net/http"
	"os"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

// This source reads only the explicitly selected provider configuration. It does
// not run credential processes, consult developer SSO caches, or use ambient
// AWS profiles/metadata. A caller freezes its result for STS and EC2 together.
type awsCredentialScope struct {
	values   map[string]string
	profile  string
	client   aws.HTTPClient
	endpoint string // Set only by local API fixtures, never from the environment.
}

func (*awsCredentialScope) String() string   { return "AWS credentials (redacted)" }
func (*awsCredentialScope) GoString() string { return "AWS credentials (redacted)" }

func (s *awsCredentialScope) retrieve(ctx context.Context, region string) (aws.Credentials, error) {
	if err := ctx.Err(); err != nil {
		return aws.Credentials{}, err
	}
	if s.values["AWS_ACCESS_KEY_ID"] != "" {
		return aws.Credentials{AccessKeyID: s.values["AWS_ACCESS_KEY_ID"], SecretAccessKey: s.values["AWS_SECRET_ACCESS_KEY"], SessionToken: s.values["AWS_SESSION_TOKEN"], Source: "RunnerScout explicit credentials"}, nil
	}
	if s.values["AWS_WEB_IDENTITY_TOKEN_FILE"] != "" {
		return s.web(ctx, region, s.values["AWS_ROLE_ARN"], s.values["AWS_WEB_IDENTITY_TOKEN_FILE"], "runnerscout")
	}
	profile := s.profile
	if profile == "" {
		profile = "default"
	}
	files, credentials := []string{}, []string{}
	if file := s.values["AWS_CONFIG_FILE"]; file != "" {
		files = append(files, file)
	}
	if file := s.values["AWS_SHARED_CREDENTIALS_FILE"]; file != "" {
		credentials = append(credentials, file)
	}
	if len(files)+len(credentials) == 0 {
		return aws.Credentials{}, errors.New("AWS credentials require explicit files, static keys or web identity")
	}
	// The shared-config loader ignores missing files; require all selected mounts
	// to exist so a delayed/deleted projection cannot silently change identity.
	for _, file := range append(append([]string{}, files...), credentials...) {
		if _, err := os.Stat(file); err != nil {
			return aws.Credentials{}, errors.New("AWS credential file unavailable")
		}
	}
	config, err := awsconfig.LoadSharedConfigProfile(ctx, profile, func(options *awsconfig.LoadSharedConfigOptions) {
		options.ConfigFiles, options.CredentialsFiles = files, credentials
	})
	if err != nil {
		return aws.Credentials{}, errors.New("AWS credential profile invalid")
	}
	return s.shared(ctx, region, config, 0)
}

func (s *awsCredentialScope) config(region string, credentials aws.CredentialsProvider) aws.Config {
	client := s.client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	return aws.Config{Region: region, Credentials: credentials, HTTPClient: client, RetryMaxAttempts: 1}
}

func (s *awsCredentialScope) sts(config aws.Config) *sts.Client {
	return sts.NewFromConfig(config, func(options *sts.Options) {
		if s.endpoint != "" {
			options.BaseEndpoint = aws.String(s.endpoint)
		}
	})
}

func (s *awsCredentialScope) web(ctx context.Context, region, role, file, session string) (aws.Credentials, error) {
	if role == "" || file == "" {
		return aws.Credentials{}, errors.New("AWS web identity configuration incomplete")
	}
	provider := stscreds.NewWebIdentityRoleProvider(s.sts(s.config(region, aws.AnonymousCredentials{})), role, stscreds.IdentityTokenFile(file), func(options *stscreds.WebIdentityRoleOptions) {
		options.RoleSessionName = session
	})
	value, err := provider.Retrieve(ctx)
	if ctx.Err() != nil {
		return aws.Credentials{}, ctx.Err()
	}
	if err != nil {
		return aws.Credentials{}, errors.New("AWS web identity exchange unavailable")
	}
	return value, nil
}

func (s *awsCredentialScope) shared(ctx context.Context, region string, config awsconfig.SharedConfig, depth int) (aws.Credentials, error) {
	if depth > 8 || config.CredentialProcess != "" || config.CredentialSource != "" || config.SSOSessionName != "" || config.SSOStartURL != "" || config.SSOAccountID != "" || config.MFASerial != "" {
		return aws.Credentials{}, errors.New("unsupported AWS credential profile source")
	}
	if config.WebIdentityTokenFile != "" {
		return s.web(ctx, region, config.RoleARN, config.WebIdentityTokenFile, config.RoleSessionName)
	}
	if config.RoleARN == "" {
		if !config.Credentials.HasKeys() {
			return aws.Credentials{}, errors.New("AWS profile credentials unavailable")
		}
		return config.Credentials, nil
	}
	base := config.Credentials
	if config.Source != nil {
		var err error
		base, err = s.shared(ctx, region, *config.Source, depth+1)
		if err != nil {
			return aws.Credentials{}, err
		}
	}
	if !base.HasKeys() {
		return aws.Credentials{}, errors.New("AWS role source credentials unavailable")
	}
	provider := stscreds.NewAssumeRoleProvider(s.sts(s.config(region, frozenAWSCredentials(base))), config.RoleARN, func(options *stscreds.AssumeRoleOptions) {
		options.RoleSessionName = config.RoleSessionName
		if config.ExternalID != "" {
			options.ExternalID = aws.String(config.ExternalID)
		}
		if config.RoleDurationSeconds != nil {
			options.Duration = *config.RoleDurationSeconds
		}
	})
	value, err := provider.Retrieve(ctx)
	if ctx.Err() != nil {
		return aws.Credentials{}, ctx.Err()
	}
	if err != nil {
		return aws.Credentials{}, errors.New("AWS role exchange unavailable")
	}
	return value, nil
}

// A credential rotation between account verification and the cloud effect must
// not cause the effect to execute under an identity different from the verified one.
func frozenAWSCredentials(value aws.Credentials) aws.CredentialsProvider {
	return aws.CredentialsProviderFunc(func(ctx context.Context) (aws.Credentials, error) {
		if err := ctx.Err(); err != nil {
			return aws.Credentials{}, err
		}
		if !value.HasKeys() || value.Expired() {
			return aws.Credentials{}, errors.New("AWS credentials unavailable or expired")
		}
		return value, nil
	})
}
