// Package sigv4 implements AWS Signature Version 4 request signing for
// non-streaming HTTP request bodies.
//
// Reference: https://docs.aws.amazon.com/general/latest/gr/sigv4-signing.html
//
// Scope:
//   - Sign an http.Request given access key, secret, optional session token,
//     region, service, and a fixed point in time.
//   - Body must be in-memory (request.Body must be a *bytes.Buffer/Reader, or
//     nil); the caller is responsible for setting the body before signing.
//
// Out of scope:
//   - Streaming payload signing (STREAMING-AWS4-HMAC-SHA256-PAYLOAD).
//   - Presigned URLs.
//   - Chunked / Transfer-Encoding.
package sigv4

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// Credentials are the inputs to the signer.
type Credentials struct {
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string // optional
}

// Signer signs HTTP requests with SigV4.
type Signer struct {
	Region  string
	Service string
}

// emptyPayloadHash is sha256("").
const emptyPayloadHash = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

// Sign mutates req by adding the Authorization, X-Amz-Date, X-Amz-Content-Sha256,
// and (if present) X-Amz-Security-Token headers. Body is read fully and replaced
// with a fresh bytes.Reader so the caller can still send the request.
func (s *Signer) Sign(req *http.Request, creds Credentials, t time.Time) error {
	if creds.AccessKeyID == "" || creds.SecretAccessKey == "" {
		return errors.New("sigv4: missing credentials")
	}
	if s.Region == "" || s.Service == "" {
		return errors.New("sigv4: missing region or service")
	}
	if req.URL == nil || req.URL.Host == "" {
		return errors.New("sigv4: request URL must have a host")
	}

	t = t.UTC()
	amzDate := t.Format("20060102T150405Z")
	dateStamp := t.Format("20060102")

	// Body hash.
	payloadHash, err := hashBody(req)
	if err != nil {
		return err
	}

	// Required headers.
	if req.Header == nil {
		req.Header = http.Header{}
	}
	req.Header.Set("Host", req.URL.Host)
	req.Header.Set("X-Amz-Date", amzDate)
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)
	if creds.SessionToken != "" {
		req.Header.Set("X-Amz-Security-Token", creds.SessionToken)
	}

	signedHeaders, canonHeaders := canonicalHeaders(req)

	canonReq := strings.Join([]string{
		req.Method,
		canonicalURI(req.URL),
		canonicalQuery(req.URL),
		canonHeaders,
		signedHeaders,
		payloadHash,
	}, "\n")

	scope := dateStamp + "/" + s.Region + "/" + s.Service + "/aws4_request"
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		amzDate,
		scope,
		hexSha256([]byte(canonReq)),
	}, "\n")

	signingKey := deriveSigningKey(creds.SecretAccessKey, dateStamp, s.Region, s.Service)
	signature := hex.EncodeToString(hmacSHA256(signingKey, []byte(stringToSign)))

	auth := fmt.Sprintf(
		"AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		creds.AccessKeyID, scope, signedHeaders, signature,
	)
	req.Header.Set("Authorization", auth)
	return nil
}

// hashBody reads the request body, replaces it with a buffered copy, and returns
// the hex sha256 of the bytes.
func hashBody(req *http.Request) (string, error) {
	if req.Body == nil || req.Body == http.NoBody {
		return emptyPayloadHash, nil
	}
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return "", fmt.Errorf("sigv4: read body: %w", err)
	}
	if err := req.Body.Close(); err != nil {
		return "", fmt.Errorf("sigv4: close body: %w", err)
	}
	req.Body = io.NopCloser(bytes.NewReader(body))
	req.ContentLength = int64(len(body))
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(body)), nil
	}
	return hexSha256(body), nil
}

// canonicalURI returns the URI path encoded per SigV4 (encode each segment).
func canonicalURI(u *url.URL) string {
	// Canonicalise from the *escaped* path, not u.Path. SigV4 for non-S3
	// services double-encodes the path: any percent-escape already present in
	// the request URI (e.g. a `/` inside an ARN encoded as %2F) must be encoded
	// again to %252F. Using u.Path would decode %2F back to a literal `/`, which
	// would then be treated as a path separator and signed as a bare `/`,
	// breaking the signature for ARNs (application-inference-profile etc.).
	path := u.EscapedPath()
	if path == "" {
		return "/"
	}
	// AWS canonicalisation: each path segment is URL-encoded (RFC 3986
	// unreserved set), but the segment separators stay as `/`.
	parts := strings.Split(path, "/")
	for i, p := range parts {
		parts[i] = awsPathEscape(p)
	}
	return strings.Join(parts, "/")
}

// awsPathEscape percent-encodes a single path segment per SigV4 rules:
// unreserved = ALPHA / DIGIT / "-" / "_" / "." / "~"; everything else is
// %HH-encoded with uppercase hex.
func awsPathEscape(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if isUnreserved(c) {
			b.WriteByte(c)
			continue
		}
		fmt.Fprintf(&b, "%%%02X", c)
	}
	return b.String()
}

func isUnreserved(c byte) bool {
	switch {
	case 'A' <= c && c <= 'Z':
		return true
	case 'a' <= c && c <= 'z':
		return true
	case '0' <= c && c <= '9':
		return true
	}
	return c == '-' || c == '_' || c == '.' || c == '~'
}

// canonicalQuery returns query parameters sorted by key, then value, with
// AWS-style percent-encoding (space -> %20, etc.).
func canonicalQuery(u *url.URL) string {
	if u.RawQuery == "" {
		return ""
	}
	values := u.Query()
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		vs := append([]string(nil), values[k]...)
		sort.Strings(vs)
		ek := awsPathEscape(k)
		for _, v := range vs {
			parts = append(parts, ek+"="+awsPathEscape(v))
		}
	}
	return strings.Join(parts, "&")
}

// canonicalHeaders returns ("signed;header;list", "canonical-headers-string").
// All headers in req.Header are signed (SigV4 allows subsetting; we sign all
// to keep the integrity guarantee strong).
func canonicalHeaders(req *http.Request) (signed, canon string) {
	keys := make([]string, 0, len(req.Header)+1)
	for k := range req.Header {
		keys = append(keys, strings.ToLower(k))
	}
	sort.Strings(keys)

	var b strings.Builder
	for _, k := range keys {
		// http.Header is keyed canonical; look up via the original casing by
		// trying both. The textproto package canonicalises to "Foo-Bar" form.
		vals := req.Header.Values(http.CanonicalHeaderKey(k))
		trimmed := make([]string, len(vals))
		for i, v := range vals {
			trimmed[i] = trimSequentialSpaces(v)
		}
		b.WriteString(k)
		b.WriteByte(':')
		b.WriteString(strings.Join(trimmed, ","))
		b.WriteByte('\n')
	}
	return strings.Join(keys, ";"), b.String()
}

// trimSequentialSpaces collapses runs of spaces inside a header value and trims
// leading/trailing whitespace.
func trimSequentialSpaces(s string) string {
	s = strings.TrimSpace(s)
	var b strings.Builder
	b.Grow(len(s))
	prevSpace := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == ' ' || c == '\t' {
			if !prevSpace {
				b.WriteByte(' ')
				prevSpace = true
			}
			continue
		}
		b.WriteByte(c)
		prevSpace = false
	}
	return b.String()
}

func hexSha256(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func hmacSHA256(key, data []byte) []byte {
	m := hmac.New(sha256.New, key)
	m.Write(data)
	return m.Sum(nil)
}

func deriveSigningKey(secret, dateStamp, region, service string) []byte {
	kDate := hmacSHA256([]byte("AWS4"+secret), []byte(dateStamp))
	kRegion := hmacSHA256(kDate, []byte(region))
	kService := hmacSHA256(kRegion, []byte(service))
	return hmacSHA256(kService, []byte("aws4_request"))
}
