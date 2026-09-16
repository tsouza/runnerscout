package provider

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	compute "google.golang.org/api/compute/v1"
	"google.golang.org/api/option"
)

func gcpCredential(ctx context.Context, environment map[string]string) (oauth2.TokenSource, error) {
	file, alias := environment["GOOGLE_APPLICATION_CREDENTIALS"], environment["CLOUDSDK_AUTH_CREDENTIAL_FILE_OVERRIDE"]
	if file != "" && alias != "" && file != alias {
		return nil, errors.New("ambiguous GCP credential files")
	}
	if file == "" {
		file = alias
	}
	if file == "" {
		return google.ComputeTokenSource("", compute.ComputeScope), nil
	}
	data, err := readGCPCredential(file)
	if err != nil {
		return nil, err
	}
	if _, err := parseGCPCredential(ctx, data); err != nil {
		return nil, err
	}
	client, _ := ctx.Value(oauth2.HTTPClient).(*http.Client)
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	return &gcpFileCredential{file: file, client: client, gate: make(chan struct{}, 1)}, nil
}
func readGCPCredential(file string) ([]byte, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, errors.New("GCP credential file unavailable")
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if err != nil || len(data) > 1<<20 {
		return nil, errors.New("GCP credential file invalid")
	}
	return data, nil
}
func parseGCPCredential(ctx context.Context, data []byte) (oauth2.TokenSource, error) {
	var document map[string]any
	if json.Unmarshal(data, &document) != nil || !gcpCredentialDocument(document, 0) {
		return nil, errors.New("unsupported GCP credential configuration")
	}
	kind, _ := document["type"].(string)
	switch google.CredentialsType(kind) {
	case google.ServiceAccount, google.AuthorizedUser, google.ExternalAccount, google.ImpersonatedServiceAccount:
	default:
		return nil, errors.New("unsupported GCP credential type")
	}
	credentials, err := google.CredentialsFromJSONWithType(ctx, data, google.CredentialsType(kind), compute.ComputeScope)
	if err != nil {
		return nil, errors.New("GCP credential configuration invalid")
	}
	return credentials.TokenSource, nil
}

// Credential JSON may describe a token source, never a process to execute.
func gcpCredentialDocument(document map[string]any, depth int) bool {
	if depth > 3 {
		return false
	}
	if source, ok := document["credential_source"].(map[string]any); ok {
		if _, executable := source["executable"]; executable {
			return false
		}
	}
	if source, ok := document["source_credentials"].(map[string]any); ok {
		return gcpCredentialDocument(source, depth+1)
	}
	return true
}

// A projected Secret can change after its API revision has triggered a reload.
// Check the file at each use; cache only tokens for those exact credential bytes.
// Invalid updates never reuse a previously valid token or another identity.
type gcpFileCredential struct {
	file        string
	client      *http.Client
	gate        chan struct{}
	fingerprint [32]byte
	token       *oauth2.Token
}

func (*gcpFileCredential) String() string   { return "GCP file credentials (redacted)" }
func (*gcpFileCredential) GoString() string { return "GCP file credentials (redacted)" }
func (s *gcpFileCredential) Token() (*oauth2.Token, error) {
	return s.TokenContext(context.Background())
}
func (s *gcpFileCredential) TokenContext(ctx context.Context) (*oauth2.Token, error) {
	select {
	case s.gate <- struct{}{}:
		defer func() { <-s.gate }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	data, err := readGCPCredential(s.file)
	if err != nil {
		return nil, err
	}
	fingerprint := sha256.Sum256(data)
	if fingerprint == s.fingerprint && s.token.Valid() {
		token := *s.token
		return &token, nil
	}
	// The OAuth JWT helper uses PostForm without attaching its supplied context.
	// Bind its transport to this bounded exchange, including response body reads.
	timeout := 30 * time.Second
	if s.client.Timeout > 0 && s.client.Timeout < timeout {
		timeout = s.client.Timeout
	}
	exchangeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	client := *s.client
	base := client.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	client.Transport = &gcpExchangeTransport{ctx: exchangeCtx, base: base}
	client.Timeout = 0 // The exchange context owns the complete timeout.
	source, err := parseGCPCredential(context.WithValue(exchangeCtx, oauth2.HTTPClient, &client), data)
	if err != nil {
		return nil, err
	}
	token, err := source.Token()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil || !token.Valid() {
		return nil, errors.New("GCP token exchange unavailable")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.fingerprint, s.token = fingerprint, token
	copy := *token
	return &copy, nil
}

type gcpExchangeTransport struct {
	ctx  context.Context
	base http.RoundTripper
}

func (t *gcpExchangeTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return t.base.RoundTrip(request.Clone(t.ctx))
}

type gcpAuthTransport struct {
	source oauth2.TokenSource
	base   http.RoundTripper
}

func (t *gcpAuthTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	forwarded := false
	defer func() {
		if !forwarded && request.Body != nil {
			request.Body.Close()
		}
	}()
	if err := request.Context().Err(); err != nil {
		return nil, err
	}
	var token *oauth2.Token
	var err error
	if source, ok := t.source.(interface {
		TokenContext(context.Context) (*oauth2.Token, error)
	}); ok {
		token, err = source.TokenContext(request.Context())
	} else {
		token, err = t.source.Token()
	}
	if request.Context().Err() != nil {
		return nil, request.Context().Err()
	}
	if err != nil || !token.Valid() {
		return nil, errors.New("GCP authentication unavailable")
	}
	if err := request.Context().Err(); err != nil {
		return nil, err
	}
	authenticated := request.Clone(request.Context())
	token.SetAuthHeader(authenticated)
	forwarded = true
	return t.base.RoundTrip(authenticated)
}
func gcpHTTPClient(ctx context.Context, source oauth2.TokenSource) *http.Client {
	base := http.DefaultTransport
	if client, ok := ctx.Value(oauth2.HTTPClient).(*http.Client); ok && client.Transport != nil {
		base = client.Transport
	}
	// oauth2.NewClient would add another token cache above file change detection.
	return &http.Client{Transport: &gcpAuthTransport{source: source, base: base}, Timeout: 30 * time.Second}
}
func newGCPSDK(environment map[string]string) (*GCPSDK, error) {
	ctx := context.Background()
	credential, err := gcpCredential(ctx, environment)
	if err != nil {
		return nil, err
	}
	service, err := compute.NewService(ctx, option.WithHTTPClient(gcpHTTPClient(ctx, credential)))
	if err != nil {
		return nil, errors.New("GCP client configuration invalid")
	}
	return &GCPSDK{Service: service, BillingAPIKey: environment["GCP_BILLING_API_KEY"]}, nil
}
