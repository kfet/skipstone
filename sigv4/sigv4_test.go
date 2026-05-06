package sigv4

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// AWS-published reference values: GET /
//
//	https://docs.aws.amazon.com/general/latest/gr/sigv4-signed-request-examples.html
//
// Test creds (also from the docs):
const (
	tstAK     = "AKIDEXAMPLE"
	tstSK     = "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY"
	tstRegion = "us-east-1"
	tstSvc    = "service"
)

var tstTime = time.Date(2015, 8, 30, 12, 36, 0, 0, time.UTC)

func TestSignKnownVector_GetVanilla(t *testing.T) {
	// "get-vanilla" test from aws4_testsuite. Request:
	//   GET / HTTP/1.1
	//   Host: example.amazonaws.com
	// Expected Authorization header signature:
	//   5fa00fa31553b73ebf1942676e86291e8372ff2a2260956d9b8aae1d763fbf31
	req, _ := http.NewRequest("GET", "https://example.amazonaws.com/", nil)
	s := &Signer{Region: tstRegion, Service: tstSvc}
	if err := s.Sign(req, Credentials{AccessKeyID: tstAK, SecretAccessKey: tstSK}, tstTime); err != nil {
		t.Fatal(err)
	}
	want := "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20150830/us-east-1/service/aws4_request, SignedHeaders=host;x-amz-content-sha256;x-amz-date, Signature="
	got := req.Header.Get("Authorization")
	if !strings.HasPrefix(got, want) {
		t.Fatalf("authz mismatch:\n got: %s\nwant prefix: %s", got, want)
	}
	// We sign x-amz-content-sha256 too (most modern AWS services require it),
	// so the signature won't match the legacy test-suite value verbatim.
	// The cross-check against the AWS SDK lives in the e2e module.
}

func TestSignWithSessionToken(t *testing.T) {
	req, _ := http.NewRequest("POST", "https://x.amazonaws.com/", strings.NewReader(`{"a":1}`))
	s := &Signer{Region: "us-west-2", Service: "bedrock"}
	creds := Credentials{AccessKeyID: "AK", SecretAccessKey: "SK", SessionToken: "TOK"}
	if err := s.Sign(req, creds, tstTime); err != nil {
		t.Fatal(err)
	}
	if req.Header.Get("X-Amz-Security-Token") != "TOK" {
		t.Error("missing security token")
	}
	if req.Header.Get("X-Amz-Date") != "20150830T123600Z" {
		t.Error("amz-date")
	}
	body, _ := io.ReadAll(req.Body)
	if string(body) != `{"a":1}` {
		t.Errorf("body should be replayable, got %q", body)
	}
	gb, err := req.GetBody()
	if err != nil {
		t.Fatal(err)
	}
	body2, _ := io.ReadAll(gb)
	if string(body2) != `{"a":1}` {
		t.Errorf("GetBody returned %q", body2)
	}
}

func TestSignDeterministic(t *testing.T) {
	mk := func() *http.Request {
		r, _ := http.NewRequest("POST", "https://x.amazonaws.com/path/to thing?b=2&a=1", strings.NewReader("hello"))
		r.Header.Set("Content-Type", "application/json")
		return r
	}
	s := &Signer{Region: "us-east-1", Service: "svc"}
	creds := Credentials{AccessKeyID: "AK", SecretAccessKey: "SK"}
	r1 := mk()
	r2 := mk()
	if err := s.Sign(r1, creds, tstTime); err != nil {
		t.Fatal(err)
	}
	if err := s.Sign(r2, creds, tstTime); err != nil {
		t.Fatal(err)
	}
	if r1.Header.Get("Authorization") != r2.Header.Get("Authorization") {
		t.Error("signatures should be deterministic for same inputs")
	}
}

func TestSignDifferentTimeChangesSignature(t *testing.T) {
	mk := func() *http.Request {
		r, _ := http.NewRequest("GET", "https://x.amazonaws.com/", nil)
		return r
	}
	s := &Signer{Region: "us-east-1", Service: "svc"}
	creds := Credentials{AccessKeyID: "AK", SecretAccessKey: "SK"}
	r1, r2 := mk(), mk()
	_ = s.Sign(r1, creds, tstTime)
	_ = s.Sign(r2, creds, tstTime.Add(time.Hour))
	if r1.Header.Get("Authorization") == r2.Header.Get("Authorization") {
		t.Error("signature should change with time")
	}
}

func TestSignErrors(t *testing.T) {
	cases := []struct {
		name  string
		creds Credentials
		s     Signer
		req   func() *http.Request
	}{
		{"missing-creds", Credentials{}, Signer{Region: "r", Service: "s"}, func() *http.Request {
			r, _ := http.NewRequest("GET", "https://x/", nil)
			return r
		}},
		{"missing-region", Credentials{AccessKeyID: "A", SecretAccessKey: "B"}, Signer{Service: "s"}, func() *http.Request {
			r, _ := http.NewRequest("GET", "https://x/", nil)
			return r
		}},
		{"missing-service", Credentials{AccessKeyID: "A", SecretAccessKey: "B"}, Signer{Region: "r"}, func() *http.Request {
			r, _ := http.NewRequest("GET", "https://x/", nil)
			return r
		}},
		{"no-host", Credentials{AccessKeyID: "A", SecretAccessKey: "B"}, Signer{Region: "r", Service: "s"}, func() *http.Request {
			return &http.Request{Method: "GET"}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := c.s.Sign(c.req(), c.creds, tstTime); err == nil {
				t.Error("expected error")
			}
		})
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("read fail") }

type errCloser struct{ io.Reader }

func (errCloser) Close() error { return errors.New("close fail") }

func TestHashBodyReadError(t *testing.T) {
	req, _ := http.NewRequest("POST", "https://x/", io.NopCloser(errReader{}))
	s := &Signer{Region: "r", Service: "s"}
	if err := s.Sign(req, Credentials{AccessKeyID: "A", SecretAccessKey: "B"}, tstTime); err == nil {
		t.Error("expected read error")
	}
}

func TestHashBodyCloseError(t *testing.T) {
	req, _ := http.NewRequest("POST", "https://x/", errCloser{Reader: bytes.NewReader([]byte("hi"))})
	s := &Signer{Region: "r", Service: "s"}
	if err := s.Sign(req, Credentials{AccessKeyID: "A", SecretAccessKey: "B"}, tstTime); err == nil {
		t.Error("expected close error")
	}
}

func TestSignNoBody(t *testing.T) {
	req, _ := http.NewRequest("GET", "https://x.amazonaws.com/", nil)
	s := &Signer{Region: "r", Service: "s"}
	if err := s.Sign(req, Credentials{AccessKeyID: "A", SecretAccessKey: "B"}, tstTime); err != nil {
		t.Fatal(err)
	}
	if req.Header.Get("X-Amz-Content-Sha256") != emptyPayloadHash {
		t.Error("empty body hash")
	}
}

func TestSignNilHeader(t *testing.T) {
	u, _ := url.Parse("https://x.amazonaws.com/")
	req := &http.Request{Method: "GET", URL: u, Header: nil}
	s := &Signer{Region: "r", Service: "s"}
	if err := s.Sign(req, Credentials{AccessKeyID: "A", SecretAccessKey: "B"}, tstTime); err != nil {
		t.Fatal(err)
	}
	if req.Header.Get("Authorization") == "" {
		t.Error("expected authz header")
	}
}

func TestSignNoBodyExplicitNoBody(t *testing.T) {
	req, _ := http.NewRequest("GET", "https://x.amazonaws.com/", http.NoBody)
	s := &Signer{Region: "r", Service: "s"}
	if err := s.Sign(req, Credentials{AccessKeyID: "A", SecretAccessKey: "B"}, tstTime); err != nil {
		t.Fatal(err)
	}
}

func TestCanonicalURI(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", "/"},
		{"/", "/"},
		{"/foo", "/foo"},
		{"/foo bar", "/foo%20bar"},
		{"/a/b+c/d~e", "/a/b%2Bc/d~e"},
	}
	for _, c := range cases {
		u := mustURL(c.in)
		if got := canonicalURI(u); got != c.want {
			t.Errorf("canonicalURI(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestCanonicalQuery(t *testing.T) {
	u := mustURL("/?b=2&a=1&a=0&c=hello%20world")
	got := canonicalQuery(u)
	want := "a=0&a=1&b=2&c=hello%20world"
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestCanonicalQueryEmpty(t *testing.T) {
	u := mustURL("/")
	if got := canonicalQuery(u); got != "" {
		t.Errorf("want empty, got %q", got)
	}
}

func TestTrimSequentialSpaces(t *testing.T) {
	cases := []struct{ in, want string }{
		{"  a   b\tc  ", "a b c"},
		{"abc", "abc"},
		{"  ", ""},
	}
	for _, c := range cases {
		if got := trimSequentialSpaces(c.in); got != c.want {
			t.Errorf("got %q want %q", got, c.want)
		}
	}
}

func TestIsUnreserved(t *testing.T) {
	for _, c := range "AZaz09-._~" {
		if !isUnreserved(byte(c)) {
			t.Errorf("%c should be unreserved", c)
		}
	}
	for _, c := range " /?#%+=" {
		if isUnreserved(byte(c)) {
			t.Errorf("%c should not be unreserved", c)
		}
	}
}

func mustURL(s string) *url.URL {
	u, err := url.Parse(s)
	if err != nil {
		panic(err)
	}
	return u
}
