package provider

import (
	"errors"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/cloud"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
)

var credentialVariables = map[string][]string{
	"aws":   {"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN", "AWS_SHARED_CREDENTIALS_FILE", "AWS_CONFIG_FILE", "AWS_WEB_IDENTITY_TOKEN_FILE", "AWS_ROLE_ARN"},
	"azure": {"AZURE_CLIENT_ID", "AZURE_TENANT_ID", "AZURE_FEDERATED_TOKEN_FILE", "AZURE_CLIENT_SECRET", "AZURE_CLIENT_CERTIFICATE_PATH", "AZURE_CLIENT_CERTIFICATE_PASSWORD", "AZURE_CLIENT_SEND_CERTIFICATE_CHAIN"},
	"gcp":   {"GOOGLE_APPLICATION_CREDENTIALS", "CLOUDSDK_AUTH_CREDENTIAL_FILE_OVERRIDE"},
}

// CredentialVariable is shared by CRD validation and the execution boundary.
func CredentialVariable(kind, name string) bool {
	return slices.Contains(credentialVariables[kind], name)
}

// NewCommand creates a provider-local authentication scope. A nil environment
// selects the deployment's authentication variables for this provider kind only;
// a non-nil map is a complete replacement, never merged with another identity.
// Credential values remain in memory or mounted files, never process-wide env.
func NewCommand(c Config, environment map[string]string) (*Command, func() error, error) {
	p := &Command{Config: c}
	if err := p.Validate(); err != nil {
		return nil, nil, err
	}
	values := map[string]string{}
	if environment == nil {
		for _, name := range credentialVariables[c.Kind] {
			if value, exists := os.LookupEnv(name); exists {
				values[name] = value
			}
		}
	} else {
		for name, value := range environment {
			values[name] = value
		}
	}
	for name, value := range values {
		if !CredentialVariable(c.Kind, name) || value == "" || strings.ContainsRune(value, 0) {
			return nil, nil, errors.New("invalid provider credential environment")
		}
	}
	if c.Kind == "azure" {
		credential, err := azureCredential(values)
		if err != nil {
			return nil, nil, errors.New("Azure credential configuration invalid")
		}
		p.Azure = &AzureSDK{Credential: credential}
		return p, func() error { return nil }, nil
	}
	if c.Kind == "aws" {
		static := values["AWS_ACCESS_KEY_ID"] != "" || values["AWS_SECRET_ACCESS_KEY"] != "" || values["AWS_SESSION_TOKEN"] != ""
		web := values["AWS_ROLE_ARN"] != "" || values["AWS_WEB_IDENTITY_TOKEN_FILE"] != ""
		files := values["AWS_SHARED_CREDENTIALS_FILE"] != "" || values["AWS_CONFIG_FILE"] != ""
		if static && (values["AWS_ACCESS_KEY_ID"] == "" || values["AWS_SECRET_ACCESS_KEY"] == "" || web || c.Profile != "" || values["AWS_SHARED_CREDENTIALS_FILE"] != "") ||
			web && (values["AWS_ROLE_ARN"] == "" || values["AWS_WEB_IDENTITY_TOKEN_FILE"] == "" || c.Profile != "" || files) ||
			c.Profile != "" && !files {
			return nil, nil, errors.New("incomplete or ambiguous AWS credentials")
		}
	}
	if c.Kind == "gcp" {
		sdk, err := newGCPSDK(values)
		if err != nil {
			return nil, nil, err
		}
		p.GCP = sdk
		return p, func() error { return nil }, nil
	}
	p.AWS = &AWSSDK{scope: &awsCredentialScope{values: values, profile: c.Profile}}
	return p, func() error { return nil }, nil
}

func azureCredential(env map[string]string) (azcore.TokenCredential, error) {
	client, tenant := env["AZURE_CLIENT_ID"], env["AZURE_TENANT_ID"]
	file, secret, certificate := env["AZURE_FEDERATED_TOKEN_FILE"], env["AZURE_CLIENT_SECRET"], env["AZURE_CLIENT_CERTIFICATE_PATH"]
	modes := 0
	for _, value := range []string{file, secret, certificate} {
		if value != "" {
			modes++
		}
	}
	if modes > 1 || modes == 1 && (client == "" || tenant == "") ||
		modes == 0 && tenant != "" || certificate == "" && (env["AZURE_CLIENT_CERTIFICATE_PASSWORD"] != "" || env["AZURE_CLIENT_SEND_CERTIFICATE_CHAIN"] != "") {
		return nil, errors.New("incomplete or ambiguous Azure credentials")
	}
	options := azcore.ClientOptions{Cloud: cloud.AzurePublic}
	switch {
	case file != "":
		return azidentity.NewWorkloadIdentityCredential(&azidentity.WorkloadIdentityCredentialOptions{
			ClientOptions: options, ClientID: client, TenantID: tenant, TokenFilePath: file})
	case secret != "":
		return azidentity.NewClientSecretCredential(tenant, client, secret, &azidentity.ClientSecretCredentialOptions{ClientOptions: options})
	case certificate != "":
		data, err := os.ReadFile(certificate)
		if err != nil {
			return nil, err
		}
		certs, key, err := azidentity.ParseCertificates(data, []byte(env["AZURE_CLIENT_CERTIFICATE_PASSWORD"]))
		if err != nil {
			return nil, err
		}
		chain := false
		if value := env["AZURE_CLIENT_SEND_CERTIFICATE_CHAIN"]; value != "" {
			chain, err = strconv.ParseBool(value)
			if err != nil {
				return nil, err
			}
		}
		return azidentity.NewClientCertificateCredential(tenant, client, certs, key, &azidentity.ClientCertificateCredentialOptions{ClientOptions: options, SendCertificateChain: chain})
	default:
		managed := &azidentity.ManagedIdentityCredentialOptions{ClientOptions: options}
		if client != "" {
			managed.ID = azidentity.ClientID(client)
		}
		return azidentity.NewManagedIdentityCredential(managed)
	}
}
