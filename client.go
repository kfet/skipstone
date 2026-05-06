package bedrocklight

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/kfet/bedrock-light/creds"
	"github.com/kfet/bedrock-light/eventstream"
	"github.com/kfet/bedrock-light/internal/awsini"
	"github.com/kfet/bedrock-light/sigv4"
)

// Client is a Bedrock ConverseStream client.
type Client struct {
	httpClient *http.Client
	creds      creds.Provider
	region     string
	endpoint   string // optional override; if empty, derive from region
	now        func() time.Time
	maxRetries int
	backoff    func(attempt int) time.Duration
	classifier RetryClassifier
}

// Option configures a Client.
type Option func(*Client)

// WithHTTPClient installs a custom *http.Client.
func WithHTTPClient(c *http.Client) Option {
	return func(cl *Client) { cl.httpClient = c }
}

// WithRegion sets the AWS region (e.g. "us-east-1").
func WithRegion(r string) Option { return func(cl *Client) { cl.region = r } }

// WithEndpoint overrides the service endpoint URL (e.g. for FIPS or LocalStack).
func WithEndpoint(u string) Option { return func(cl *Client) { cl.endpoint = u } }

// WithCredentials installs a credentials provider.
func WithCredentials(p creds.Provider) Option { return func(cl *Client) { cl.creds = p } }

// WithStaticCredentials installs static credentials.
func WithStaticCredentials(ak, sk, st string) Option {
	return WithCredentials(creds.Static(ak, sk, st))
}

// WithProfile uses the AWS shared-config chain restricted to the given profile.
func WithProfile(name string) Option {
	return WithCredentials(creds.DefaultChain(creds.Config{Profile: name}))
}

// WithNow overrides the clock (used for SigV4 timestamps; mainly for tests).
func WithNow(fn func() time.Time) Option { return func(cl *Client) { cl.now = fn } }

// WithRetries sets the number of retries on 429/5xx (default 3).
func WithRetries(n int) Option { return func(cl *Client) { cl.maxRetries = n } }

// WithBackoff sets the backoff function. Default: 100ms * 2^attempt, capped at 5s.
func WithBackoff(fn func(attempt int) time.Duration) Option {
	return func(cl *Client) { cl.backoff = fn }
}

// RetryClassifier decides whether a request should be retried. It is invoked
// after every attempt with exactly one of (resp, err) non-nil:
//
//   - On a transport-level failure, resp is nil and err is the transport error.
//   - On a server response, resp is the HTTP response and err is nil. The
//     response body has not been consumed; the classifier must not read it.
//
// The classifier is consulted only while attempts remain (see WithRetries).
type RetryClassifier func(resp *http.Response, err error) bool

// WithRetryClassifier installs a custom retry classifier. The default
// behaviour — always retry transport errors, retry HTTP 429 / 5xx — is
// preserved when this option is not used.
func WithRetryClassifier(fn RetryClassifier) Option {
	return func(cl *Client) { cl.classifier = fn }
}

// NewClient constructs a Client. Region is resolved from (in order):
// WithRegion > AWS_REGION > AWS_DEFAULT_REGION > active profile's region= > "us-east-1".
// Endpoint is resolved from (in order):
// WithEndpoint > AWS_ENDPOINT_URL_BEDROCK_RUNTIME > derived from region.
func NewClient(opts ...Option) (*Client, error) {
	c := &Client{
		httpClient: http.DefaultClient,
		now:        time.Now,
		maxRetries: 3,
		backoff:    defaultBackoff,
	}
	for _, o := range opts {
		o(c)
	}
	if c.region == "" {
		c.region = resolveRegion()
	}
	if c.endpoint == "" {
		if v := os.Getenv("AWS_ENDPOINT_URL_BEDROCK_RUNTIME"); v != "" {
			c.endpoint = v
		} else {
			c.endpoint = "https://bedrock-runtime." + c.region + ".amazonaws.com"
		}
	}
	if c.creds == nil {
		c.creds = creds.DefaultChain(creds.Config{})
	}
	return c, nil
}

// resolveRegion picks the AWS region from env vars or the active shared
// config profile, falling back to us-east-1.
func resolveRegion() string {
	r := os.Getenv("AWS_REGION")
	if r == "" {
		r = os.Getenv("AWS_DEFAULT_REGION")
	}
	if r == "" {
		r = regionFromProfile()
	}
	if r == "" {
		r = "us-east-1"
	}
	return r
}

// regionFromProfile reads region= from ~/.aws/config for AWS_PROFILE
// (or "default"). Returns "" if not found / unreadable.
func regionFromProfile() string {
	profile := os.Getenv("AWS_PROFILE")
	if profile == "" {
		profile = "default"
	}
	home, _ := os.UserHomeDir()
	cfgFile, _ := awsini.LoadConfig(filepath.Join(home, ".aws", "config"))
	return cfgFile.Get(profile, "region")
}

// ConverseStream calls the ConverseStream API and returns a Stream.
func (c *Client) ConverseStream(ctx context.Context, in *ConverseStreamInput) (*Stream, error) {
	body, err := buildRequestBody(in)
	if err != nil {
		return nil, err
	}
	endpoint := c.endpoint + "/model/" + url.PathEscape(in.ModelID) + "/converse-stream"

	var resp *http.Response
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, nil)
		if err != nil {
			return nil, err
		}
		// Fresh body reader on every attempt.
		req.Body = io.NopCloser(bytes.NewReader(body))
		req.ContentLength = int64(len(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/vnd.amazon.eventstream")

		v, err := c.creds.Retrieve(ctx)
		if err != nil {
			return nil, fmt.Errorf("bedrocklight: credentials: %w", err)
		}
		signer := &sigv4.Signer{Region: c.region, Service: "bedrock"}
		if err := signer.Sign(req, sigv4.Credentials{
			AccessKeyID:     v.AccessKeyID,
			SecretAccessKey: v.SecretAccessKey,
			SessionToken:    v.SessionToken,
		}, c.now()); err != nil {
			return nil, fmt.Errorf("bedrocklight: sign: %w", err)
		}

		resp, err = c.httpClient.Do(req)
		if err != nil {
			if attempt < c.maxRetries && ctx.Err() == nil && c.shouldRetry(nil, err) {
				if !sleep(ctx, c.backoff(attempt)) {
					return nil, ctx.Err()
				}
				continue
			}
			return nil, fmt.Errorf("bedrocklight: do: %w", err)
		}
		if attempt < c.maxRetries && c.shouldRetry(resp, nil) {
			delay := retryAfter(resp) // honor Retry-After if present
			if delay <= 0 {
				delay = c.backoff(attempt)
			}
			drainAndClose(resp)
			if !sleep(ctx, delay) {
				return nil, ctx.Err()
			}
			continue
		}
		break
	}

	if resp.StatusCode/100 != 2 {
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return nil, &APIError{StatusCode: resp.StatusCode, Body: body}
	}
	return &Stream{resp: resp, dec: eventstream.NewDecoder(resp.Body)}, nil
}
