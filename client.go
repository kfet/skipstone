package bedrocklight

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"

	"path/filepath"

	"github.com/kfet/bedrock-light/awsini"
	"github.com/kfet/bedrock-light/creds"
	"github.com/kfet/bedrock-light/eventstream"
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

// NewClient constructs a Client. Region is resolved from (in order):
// WithRegion > AWS_REGION > AWS_DEFAULT_REGION > "us-east-1".
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
		c.region = os.Getenv("AWS_REGION")
		if c.region == "" {
			c.region = os.Getenv("AWS_DEFAULT_REGION")
		}
		if c.region == "" {
			c.region = regionFromProfile()
		}
		if c.region == "" {
			c.region = "us-east-1"
		}
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

func defaultBackoff(attempt int) time.Duration {
	d := 100 * time.Millisecond
	for i := 0; i < attempt && d < 5*time.Second; i++ {
		d *= 2
	}
	if d > 5*time.Second {
		d = 5 * time.Second
	}
	return d
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
		req, err := http.NewRequestWithContext(ctx, "POST", endpoint, nil)
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
			if attempt < c.maxRetries && ctx.Err() == nil {
				if !sleep(ctx, c.backoff(attempt)) {
					return nil, ctx.Err()
				}
				continue
			}
			return nil, fmt.Errorf("bedrocklight: do: %w", err)
		}
		if shouldRetry(resp.StatusCode) && attempt < c.maxRetries {
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

// APIError is returned for non-2xx Bedrock responses.
type APIError struct {
	StatusCode int
	Body       []byte
}

// Error implements error.
func (e *APIError) Error() string {
	return "bedrocklight: HTTP " + strconv.Itoa(e.StatusCode) + ": " + string(e.Body)
}

func shouldRetry(code int) bool {
	return code == 429 || (code >= 500 && code < 600)
}

func retryAfter(resp *http.Response) time.Duration {
	v := resp.Header.Get("Retry-After")
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		return time.Until(t)
	}
	return 0
}

func drainAndClose(resp *http.Response) {
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
}

func sleep(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return true
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// Stream is a typed iterator over the events of a ConverseStream response.
type Stream struct {
	resp *http.Response
	dec  *eventstream.Decoder
}

// Event is a decoded streaming event. Exactly one pointer field is non-nil
// (Error is populated when a Bedrock-side error frame arrives).
type Event struct {
	Type     string // raw :event-type header value
	Raw      []byte // raw JSON payload, useful for forwards-compat
	Decoded  any    // decoded into one of the Event* structs below, if known
	APIError *APIError
}

// Recv returns the next event, or io.EOF at end of stream.
func (s *Stream) Recv() (Event, error) {
	for {
		f, err := s.dec.Next()
		if err != nil {
			return Event{}, err
		}
		ev := Event{
			Type: f.Get(":event-type"),
			Raw:  f.Payload,
		}
		// :message-type "exception" or "error" indicates server-side error frame.
		mt := f.Get(":message-type")
		if mt == "exception" || mt == "error" {
			ev.APIError = &APIError{StatusCode: 0, Body: f.Payload}
			return ev, nil
		}
		ev.Decoded = decodeEvent(ev.Type, ev.Raw)
		return ev, nil
	}
}

// Close releases the underlying HTTP connection.
func (s *Stream) Close() error { return s.resp.Body.Close() }

// Decoded event payloads. Only the shapes fir consumes are typed; callers
// needing more can parse Event.Raw themselves.

// EventMessageStart corresponds to :event-type messageStart.
type EventMessageStart struct {
	Role string `json:"role"`
}

// EventContentBlockStart corresponds to :event-type contentBlockStart.
type EventContentBlockStart struct {
	ContentBlockIndex int `json:"contentBlockIndex"`
	Start             struct {
		ToolUse *struct {
			ToolUseID string `json:"toolUseId"`
			Name      string `json:"name"`
		} `json:"toolUse,omitempty"`
	} `json:"start"`
}

// EventContentBlockDelta corresponds to :event-type contentBlockDelta.
type EventContentBlockDelta struct {
	ContentBlockIndex int `json:"contentBlockIndex"`
	Delta             struct {
		Text             string `json:"text,omitempty"`
		ToolUse          *struct {
			Input string `json:"input"`
		} `json:"toolUse,omitempty"`
		ReasoningContent *struct {
			Text      string `json:"text,omitempty"`
			Signature string `json:"signature,omitempty"`
		} `json:"reasoningContent,omitempty"`
	} `json:"delta"`
}

// EventContentBlockStop corresponds to :event-type contentBlockStop.
type EventContentBlockStop struct {
	ContentBlockIndex int `json:"contentBlockIndex"`
}

// EventMessageStop corresponds to :event-type messageStop.
type EventMessageStop struct {
	StopReason           string          `json:"stopReason"`
	AdditionalModelResponseFields json.RawMessage `json:"additionalModelResponseFields,omitempty"`
}

// EventMetadata corresponds to :event-type metadata.
type EventMetadata struct {
	Usage struct {
		InputTokens            int `json:"inputTokens"`
		OutputTokens           int `json:"outputTokens"`
		TotalTokens            int `json:"totalTokens"`
		CacheReadInputTokens   int `json:"cacheReadInputTokens"`
		CacheWriteInputTokens  int `json:"cacheWriteInputTokens"`
	} `json:"usage"`
	Metrics struct {
		LatencyMs int `json:"latencyMs"`
	} `json:"metrics"`
}

func decodeEvent(t string, raw []byte) any {
	switch t {
	case "messageStart":
		var v EventMessageStart
		if err := json.Unmarshal(raw, &v); err == nil {
			return v
		}
	case "contentBlockStart":
		var v EventContentBlockStart
		if err := json.Unmarshal(raw, &v); err == nil {
			return v
		}
	case "contentBlockDelta":
		var v EventContentBlockDelta
		if err := json.Unmarshal(raw, &v); err == nil {
			return v
		}
	case "contentBlockStop":
		var v EventContentBlockStop
		if err := json.Unmarshal(raw, &v); err == nil {
			return v
		}
	case "messageStop":
		var v EventMessageStop
		if err := json.Unmarshal(raw, &v); err == nil {
			return v
		}
	case "metadata":
		var v EventMetadata
		if err := json.Unmarshal(raw, &v); err == nil {
			return v
		}
	}
	return nil
}



// regionFromProfile reads region= from ~/.aws/config for AWS_PROFILE (or "default").
// Returns "" if not found / unreadable.
func regionFromProfile() string {
	profile := os.Getenv("AWS_PROFILE")
	if profile == "" {
		profile = "default"
	}
	home, _ := os.UserHomeDir()
	cfgFile, _ := awsini.LoadConfig(filepath.Join(home, ".aws", "config"))
	return cfgFile.Get(profile, "region")
}
