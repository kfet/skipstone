package skipstone

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kfet/skipstone/creds"
	"github.com/kfet/skipstone/eventstream"
)

func newTestServer(t *testing.T, h http.HandlerFunc) (*httptest.Server, *Client) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	cl, err := NewClient(
		WithEndpoint(srv.URL),
		WithRegion("us-east-1"),
		WithStaticCredentials("AK", "SK", ""),
		WithBackoff(func(int) time.Duration { return time.Millisecond }),
	)
	if err != nil {
		t.Fatal(err)
	}
	return srv, cl
}

func goodInput() *ConverseStreamInput {
	return &ConverseStreamInput{
		ModelID:  "anthropic.claude",
		Messages: []Message{{Role: RoleUser, Content: []Block{{Text: "hi"}}}},
	}
}

func TestConverseStream_HappyPath(t *testing.T) {
	frame1 := eventstream.Encode(map[string]string{":event-type": "messageStart", ":message-type": "event"}, []byte(`{"role":"assistant"}`))
	frame2 := eventstream.Encode(map[string]string{":event-type": "contentBlockDelta", ":message-type": "event"}, []byte(`{"contentBlockIndex":0,"delta":{"text":"hello"}}`))
	frame3 := eventstream.Encode(map[string]string{":event-type": "messageStop", ":message-type": "event"}, []byte(`{"stopReason":"end_turn"}`))
	frame4 := eventstream.Encode(map[string]string{":event-type": "metadata", ":message-type": "event"}, []byte(`{"usage":{"inputTokens":1,"outputTokens":2,"totalTokens":3},"metrics":{"latencyMs":42}}`))
	body := append(append(append(frame1, frame2...), frame3...), frame4...)

	_, cl := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("method: %s", r.Method)
		}
		if !strings.HasSuffix(r.URL.Path, "/converse-stream") {
			t.Errorf("path: %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") == "" {
			t.Error("missing authorization")
		}
		w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
		w.Write(body)
	})

	stream, err := cl.ConverseStream(context.Background(), goodInput())
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()

	var got []string
	for {
		ev, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, ev.Type)
		if ev.Type == "contentBlockDelta" {
			d := ev.Decoded.(EventContentBlockDelta)
			if d.Delta.Text != "hello" {
				t.Errorf("delta text: %q", d.Delta.Text)
			}
		}
		if ev.Type == "messageStop" {
			d := ev.Decoded.(EventMessageStop)
			if d.StopReason != "end_turn" {
				t.Errorf("stop reason: %q", d.StopReason)
			}
		}
		if ev.Type == "metadata" {
			d := ev.Decoded.(EventMetadata)
			if d.Usage.TotalTokens != 3 {
				t.Errorf("tokens: %+v", d.Usage)
			}
		}
	}
	if len(got) != 4 {
		t.Errorf("got events %v", got)
	}
}

func TestConverseStream_DecodesAllEventTypes(t *testing.T) {
	frames := [][]byte{
		eventstream.Encode(map[string]string{":event-type": "messageStart"}, []byte(`{"role":"assistant"}`)),
		eventstream.Encode(map[string]string{":event-type": "contentBlockStart"}, []byte(`{"contentBlockIndex":0,"start":{"toolUse":{"toolUseId":"t","name":"n"}}}`)),
		eventstream.Encode(map[string]string{":event-type": "contentBlockDelta"}, []byte(`{"contentBlockIndex":0,"delta":{"toolUse":{"input":"{}"}}}`)),
		eventstream.Encode(map[string]string{":event-type": "contentBlockDelta"}, []byte(`{"contentBlockIndex":1,"delta":{"reasoningContent":{"text":"r"}}}`)),
		eventstream.Encode(map[string]string{":event-type": "contentBlockStop"}, []byte(`{"contentBlockIndex":0}`)),
		eventstream.Encode(map[string]string{":event-type": "messageStop"}, []byte(`{"stopReason":"end_turn"}`)),
		eventstream.Encode(map[string]string{":event-type": "metadata"}, []byte(`{"usage":{},"metrics":{}}`)),
		eventstream.Encode(map[string]string{":event-type": "unknownEvent"}, []byte(`{}`)),
	}
	var body []byte
	for _, f := range frames {
		body = append(body, f...)
	}
	_, cl := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write(body)
	})
	stream, err := cl.ConverseStream(context.Background(), goodInput())
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	count := 0
	for {
		ev, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		count++
		if ev.Type == "unknownEvent" && ev.Decoded != nil {
			t.Error("unknown event should have nil Decoded")
		}
	}
	if count != len(frames) {
		t.Errorf("got %d events, want %d", count, len(frames))
	}
}

func TestConverseStream_ServerErrorFrame(t *testing.T) {
	body := eventstream.Encode(map[string]string{
		":event-type":   "modelStreamErrorException",
		":message-type": "exception",
	}, []byte(`{"message":"boom"}`))
	_, cl := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write(body)
	})
	stream, err := cl.ConverseStream(context.Background(), goodInput())
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	ev, err := stream.Recv()
	if err != nil {
		t.Fatal(err)
	}
	if ev.APIError == nil || !strings.Contains(string(ev.APIError.Body), "boom") {
		t.Errorf("expected exception, got %+v", ev)
	}
}

func TestConverseStream_BadInput(t *testing.T) {
	_, cl := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {})
	if _, err := cl.ConverseStream(context.Background(), nil); err == nil {
		t.Error("nil input")
	}
}

func TestConverseStream_Non2xx(t *testing.T) {
	_, cl := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		w.Write([]byte(`{"message":"bad"}`))
	})
	_, err := cl.ConverseStream(context.Background(), goodInput())
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected APIError, got %v", err)
	}
	if apiErr.StatusCode != 400 || !strings.Contains(apiErr.Error(), "400") {
		t.Errorf("got %+v / %s", apiErr, apiErr.Error())
	}
}

func TestConverseStream_RetryOn500(t *testing.T) {
	calls := 0
	_, cl := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls < 3 {
			w.WriteHeader(500)
			return
		}
		w.Write(eventstream.Encode(map[string]string{":event-type": "messageStop"}, []byte(`{"stopReason":"end_turn"}`)))
	})
	stream, err := cl.ConverseStream(context.Background(), goodInput())
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	if calls != 3 {
		t.Errorf("expected 3 calls, got %d", calls)
	}
}

func TestConverseStream_RetryOn429WithRetryAfter(t *testing.T) {
	calls := 0
	_, cl := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(429)
			return
		}
		if calls == 2 {
			w.Header().Set("Retry-After", time.Now().UTC().Add(time.Millisecond).Format(http.TimeFormat))
			w.WriteHeader(429)
			return
		}
		w.Write(eventstream.Encode(map[string]string{":event-type": "messageStop"}, []byte(`{}`)))
	})
	stream, err := cl.ConverseStream(context.Background(), goodInput())
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	if calls < 3 {
		t.Errorf("expected retries, got %d", calls)
	}
}

func TestConverseStream_RetryAfterUnparseable(t *testing.T) {
	calls := 0
	_, cl := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.Header().Set("Retry-After", "not-a-number")
			w.WriteHeader(503)
			return
		}
		w.Write(eventstream.Encode(map[string]string{":event-type": "messageStop"}, []byte(`{}`)))
	})
	stream, err := cl.ConverseStream(context.Background(), goodInput())
	if err != nil {
		t.Fatal(err)
	}
	stream.Close()
	if calls != 2 {
		t.Errorf("calls=%d", calls)
	}
}

func TestConverseStream_RetryGiveUp(t *testing.T) {
	calls := 0
	_, cl := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(500)
		w.Write([]byte("nope"))
	})
	_, err := cl.ConverseStream(context.Background(), goodInput())
	if err == nil {
		t.Fatal("expected error")
	}
	// Default retries = 3 -> 4 total attempts.
	if calls != 4 {
		t.Errorf("calls=%d", calls)
	}
}

func TestConverseStream_BadEndpoint(t *testing.T) {
	cl, _ := NewClient(
		WithEndpoint("http://[::1]:bad"), // unparseable URL
		WithRegion("us-east-1"),
		WithStaticCredentials("A", "B", ""),
	)
	if _, err := cl.ConverseStream(context.Background(), goodInput()); err == nil {
		t.Error("expected URL error")
	}
}

func TestConverseStream_TransportErrorThenCancel(t *testing.T) {
	// Transport always errors; cancel during backoff covers the
	// "transport error + ctx canceled" branch.
	ctx, cancel := context.WithCancel(context.Background())
	hc := &http.Client{Transport: roundTripFn(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("net fail")
	})}
	cl, _ := NewClient(
		WithEndpoint("http://x"),
		WithRegion("us-east-1"),
		WithStaticCredentials("A", "B", ""),
		WithHTTPClient(hc),
		WithBackoff(func(int) time.Duration { cancel(); return 50 * time.Millisecond }),
		WithRetries(3),
	)
	if _, err := cl.ConverseStream(ctx, goodInput()); err == nil {
		t.Error("expected error")
	}
}

type roundTripFn func(*http.Request) (*http.Response, error)

func (f roundTripFn) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestConverseStream_TransportError(t *testing.T) {
	cl, err := NewClient(
		WithEndpoint("http://127.0.0.1:1"), // refused
		WithRegion("us-east-1"),
		WithStaticCredentials("A", "B", ""),
		WithBackoff(func(int) time.Duration { return 0 }),
		WithRetries(1),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cl.ConverseStream(context.Background(), goodInput()); err == nil {
		t.Error("expected transport error")
	}
}

func TestConverseStream_ContextCanceled(t *testing.T) {
	_, cl := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := cl.ConverseStream(ctx, goodInput()); err == nil {
		t.Error("expected error")
	}
}

func TestConverseStream_ContextCanceledDuringBackoff(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(500)
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cl, _ := NewClient(
		WithEndpoint(srv.URL),
		WithRegion("us-east-1"),
		WithStaticCredentials("A", "B", ""),
		WithBackoff(func(int) time.Duration { cancel(); return 50 * time.Millisecond }),
		WithRetries(3),
	)
	if _, err := cl.ConverseStream(ctx, goodInput()); err == nil {
		t.Error("expected error")
	}
}

func TestConverseStream_CredsError(t *testing.T) {
	cl, _ := NewClient(
		WithEndpoint("http://x"),
		WithRegion("us-east-1"),
		WithCredentials(creds.ProviderFunc(func(context.Context) (creds.Value, error) {
			return creds.Value{}, errors.New("no creds")
		})),
	)
	if _, err := cl.ConverseStream(context.Background(), goodInput()); err == nil {
		t.Error("expected creds error")
	}
}

func TestConverseStream_SignError(t *testing.T) {
	cl, _ := NewClient(
		WithEndpoint("http://x"),
		WithRegion(""), // signer rejects empty
		WithStaticCredentials("A", "B", ""),
	)
	cl.region = "" // bypass NewClient's default
	if _, err := cl.ConverseStream(context.Background(), goodInput()); err == nil {
		t.Error("expected sign error")
	}
}

func TestNewClientDefaults(t *testing.T) {
	t.Setenv("AWS_REGION", "")
	t.Setenv("AWS_DEFAULT_REGION", "")
	t.Setenv("AWS_ENDPOINT_URL_BEDROCK_RUNTIME", "")
	cl, err := NewClient()
	if err != nil {
		t.Fatal(err)
	}
	if cl.region != "us-east-1" {
		t.Errorf("region: %s", cl.region)
	}
	if !strings.Contains(cl.endpoint, "bedrock-runtime.us-east-1") {
		t.Errorf("endpoint: %s", cl.endpoint)
	}
	if cl.creds == nil {
		t.Error("creds nil")
	}
}

func TestNewClient_RegionFromEnv(t *testing.T) {
	t.Setenv("AWS_REGION", "")
	t.Setenv("AWS_DEFAULT_REGION", "eu-west-1")
	t.Setenv("AWS_ENDPOINT_URL_BEDROCK_RUNTIME", "")
	cl, err := NewClient()
	if err != nil {
		t.Fatal(err)
	}
	if cl.region != "eu-west-1" {
		t.Errorf("region: %s", cl.region)
	}
}

func TestNewClient_EndpointFromEnv(t *testing.T) {
	t.Setenv("AWS_ENDPOINT_URL_BEDROCK_RUNTIME", "https://example.com")
	cl, err := NewClient()
	if err != nil {
		t.Fatal(err)
	}
	if cl.endpoint != "https://example.com" {
		t.Errorf("endpoint: %s", cl.endpoint)
	}
}

func TestWithProfile(t *testing.T) {
	cl, err := NewClient(WithProfile("foo"))
	if err != nil {
		t.Fatal(err)
	}
	if cl.creds == nil {
		t.Error("creds nil")
	}
}

func TestWithHTTPClient(t *testing.T) {
	hc := &http.Client{}
	cl, _ := NewClient(WithHTTPClient(hc))
	if cl.httpClient != hc {
		t.Error("http client not set")
	}
}

func TestWithNow(t *testing.T) {
	called := false
	cl, _ := NewClient(WithNow(func() time.Time { called = true; return time.Now() }))
	cl.now()
	if !called {
		t.Error("now not used")
	}
}

func TestDefaultBackoffCaps(t *testing.T) {
	if d := defaultBackoff(0); d != 100*time.Millisecond {
		t.Errorf("attempt 0: %v", d)
	}
	if d := defaultBackoff(20); d > 5*time.Second {
		t.Errorf("uncapped: %v", d)
	}
	if d := defaultBackoff(2); d != 400*time.Millisecond {
		t.Errorf("attempt 2: %v", d)
	}
}

func TestSleepCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if sleep(ctx, time.Hour) {
		t.Error("expected cancel to interrupt sleep")
	}
}

func TestSleepZero(t *testing.T) {
	if !sleep(context.Background(), 0) {
		t.Error("zero sleep should succeed")
	}
}

func TestSleepRuns(t *testing.T) {
	if !sleep(context.Background(), time.Millisecond) {
		t.Error("sleep should complete")
	}
}

func TestEventDecodingErrorPaths(t *testing.T) {
	// Each typed branch in decodeEvent has an Unmarshal error path; force it.
	cases := []string{"messageStart", "contentBlockStart", "contentBlockDelta", "contentBlockStop", "messageStop", "metadata"}
	for _, c := range cases {
		got := decodeEvent(c, []byte("not json"))
		if got != nil {
			t.Errorf("%s: expected nil on bad json, got %v", c, got)
		}
	}
}

func TestRetryAfterAbsoluteTime(t *testing.T) {
	// Cover the http.ParseTime branch with a valid HTTP-date.
	resp := &http.Response{Header: http.Header{}}
	resp.Header.Set("Retry-After", time.Now().UTC().Add(time.Second).Format(http.TimeFormat))
	if d := retryAfter(resp); d <= 0 {
		t.Errorf("expected positive duration, got %v", d)
	}
}

func TestRetryAfterMissing(t *testing.T) {
	if d := retryAfter(&http.Response{Header: http.Header{}}); d != 0 {
		t.Errorf("got %v", d)
	}
}

func TestStreamRecvIOError(t *testing.T) {
	// Use a server that closes the connection after partial frame.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte{0, 0, 0}) // truncated prelude
	}))
	defer srv.Close()
	cl, _ := NewClient(WithEndpoint(srv.URL), WithRegion("us-east-1"), WithStaticCredentials("A", "B", ""))
	stream, err := cl.ConverseStream(context.Background(), goodInput())
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	if _, err := stream.Recv(); err == nil {
		t.Error("expected truncation error")
	}
}

func TestEventJSONShape(t *testing.T) {
	// Spot-check that EventContentBlockDelta{} round-trips through json.
	src := EventContentBlockDelta{ContentBlockIndex: 5}
	src.Delta.Text = "x"
	b, _ := json.Marshal(src)
	if !strings.Contains(string(b), `"contentBlockIndex":5`) {
		t.Errorf("got %s", b)
	}
}

func TestRegionFromProfile(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".aws"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".aws", "config"),
		[]byte("[profile p]\nregion = ap-southeast-2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("AWS_PROFILE", "p")
	t.Setenv("AWS_REGION", "")
	t.Setenv("AWS_DEFAULT_REGION", "")
	cl, err := NewClient(WithStaticCredentials("a", "b", ""))
	if err != nil {
		t.Fatal(err)
	}
	if cl.region != "ap-southeast-2" {
		t.Errorf("got %s", cl.region)
	}
}

func TestEventMetadataCacheTokens(t *testing.T) {
	raw := []byte(`{"usage":{"inputTokens":1,"outputTokens":2,"totalTokens":3,"cacheReadInputTokens":10,"cacheWriteInputTokens":20}}`)
	v := decodeEvent("metadata", raw).(EventMetadata)
	if v.Usage.CacheReadInputTokens != 10 || v.Usage.CacheWriteInputTokens != 20 {
		t.Errorf("got %+v", v.Usage)
	}
}

func TestWithRetryClassifier_RetriesOn4xx(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(418) // not normally retried
			return
		}
		w.Write(eventstream.Encode(map[string]string{":event-type": "messageStop"}, []byte(`{}`)))
	}))
	defer srv.Close()
	cl, _ := NewClient(
		WithEndpoint(srv.URL),
		WithRegion("us-east-1"),
		WithStaticCredentials("A", "B", ""),
		WithBackoff(func(int) time.Duration { return 0 }),
		WithRetryClassifier(func(resp *http.Response, err error) bool {
			return err != nil || (resp != nil && resp.StatusCode == 418)
		}),
	)
	stream, err := cl.ConverseStream(context.Background(), goodInput())
	if err != nil {
		t.Fatal(err)
	}
	stream.Close()
	if calls != 2 {
		t.Errorf("calls=%d", calls)
	}
}

func TestWithRetryClassifier_SuppressesTransportRetry(t *testing.T) {
	calls := 0
	hc := &http.Client{Transport: roundTripFn(func(*http.Request) (*http.Response, error) {
		calls++
		return nil, errors.New("net fail")
	})}
	cl, _ := NewClient(
		WithEndpoint("http://x"),
		WithRegion("us-east-1"),
		WithStaticCredentials("A", "B", ""),
		WithHTTPClient(hc),
		WithRetries(3),
		WithRetryClassifier(func(resp *http.Response, err error) bool { return false }),
	)
	if _, err := cl.ConverseStream(context.Background(), goodInput()); err == nil {
		t.Fatal("expected error")
	}
	if calls != 1 {
		t.Errorf("classifier should have suppressed retries: calls=%d", calls)
	}
}

func TestWithHTTPTrace(t *testing.T) {
	gotStart := false
	_, cl := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write(eventstream.Encode(map[string]string{":event-type": "messageStop"}, []byte(`{}`)))
	})
	cl2, _ := NewClient(
		WithEndpoint(cl.endpoint),
		WithRegion("us-east-1"),
		WithStaticCredentials("A", "B", ""),
		WithHTTPTrace(func(ctx context.Context) *httptrace.ClientTrace {
			return &httptrace.ClientTrace{
				GetConn: func(string) { gotStart = true },
			}
		}),
	)
	stream, err := cl2.ConverseStream(context.Background(), goodInput())
	if err != nil {
		t.Fatal(err)
	}
	stream.Close()
	if !gotStart {
		t.Error("httptrace hook not called")
	}
}

func TestWithHTTPTrace_NilFactoryResult(t *testing.T) {
	// Factory returning nil must be a no-op (no panic, no context wrap).
	_, cl := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write(eventstream.Encode(map[string]string{":event-type": "messageStop"}, []byte(`{}`)))
	})
	cl2, _ := NewClient(
		WithEndpoint(cl.endpoint),
		WithRegion("us-east-1"),
		WithStaticCredentials("A", "B", ""),
		WithHTTPTrace(func(ctx context.Context) *httptrace.ClientTrace { return nil }),
	)
	stream, err := cl2.ConverseStream(context.Background(), goodInput())
	if err != nil {
		t.Fatal(err)
	}
	stream.Close()
}

func TestEventReasoningRedacted(t *testing.T) {
	raw := []byte(`{"contentBlockIndex":0,"delta":{"reasoningContent":{"redactedContent":"YWJj"}}}`)
	v := decodeEvent("contentBlockDelta", raw).(EventContentBlockDelta)
	if string(v.Delta.ReasoningContent.RedactedContent) != "abc" {
		t.Errorf("redacted: %q", v.Delta.ReasoningContent.RedactedContent)
	}
}

func TestEventCitationDelta(t *testing.T) {
	raw := []byte(`{"contentBlockIndex":0,"delta":{"citation":{"title":"doc","source":"s3://x"}}}`)
	v := decodeEvent("contentBlockDelta", raw).(EventContentBlockDelta)
	if !strings.Contains(string(v.Delta.Citation), `"title":"doc"`) {
		t.Errorf("citation: %s", v.Delta.Citation)
	}
}

func TestEventMetadataExtras(t *testing.T) {
	raw := []byte(`{"usage":{"inputTokens":1},"metrics":{"latencyMs":2},"trace":{"guardrail":{}},"performanceConfig":{"latency":"standard"},"serviceTier":"priority"}`)
	v := decodeEvent("metadata", raw).(EventMetadata)
	if v.ServiceTier != "priority" {
		t.Errorf("tier: %q", v.ServiceTier)
	}
	if !strings.Contains(string(v.Trace), "guardrail") {
		t.Errorf("trace: %s", v.Trace)
	}
	if !strings.Contains(string(v.PerformanceConfig), "standard") {
		t.Errorf("perf: %s", v.PerformanceConfig)
	}
}
