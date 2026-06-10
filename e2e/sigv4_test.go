// SigV4 cross-check: sign the same request with skipstone and the AWS SDK
// SigV4 signer; the Authorization headers must match byte-for-byte.
package e2e

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	awsv4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"

	"github.com/kfet/skipstone/sigv4"
)

func TestSigV4MatchesAWSSDK(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":[{"text":"hi"}]}]}`)
	mkReq := func() *http.Request {
		r, _ := http.NewRequest(
			"POST",
			"https://bedrock-runtime.us-east-1.amazonaws.com/model/anthropic.claude/converse-stream",
			io.NopCloser(bytes.NewReader(body)),
		)
		r.Header.Set("Content-Type", "application/json")
		return r
	}
	now := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	creds := struct {
		ak, sk, st string
	}{"AKIDEXAMPLE", "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY", ""}

	// skipstone
	light := mkReq()
	if err := (&sigv4.Signer{Region: "us-east-1", Service: "bedrock"}).Sign(
		light, sigv4.Credentials{AccessKeyID: creds.ak, SecretAccessKey: creds.sk}, now,
	); err != nil {
		t.Fatal(err)
	}

	// AWS SDK signer (same payload hash as skipstone: hex-sha256(body))
	sdkReq := mkReq()
	// AWS SDK requires Host explicitly; mirror what our signer does.
	sdkReq.Header.Set("Host", sdkReq.URL.Host)
	payloadHash := hexSHA256(body)
	// skipstone always sets x-amz-content-sha256; pre-set it on the SDK
	// request so the SDK signer covers it too.
	sdkReq.Header.Set("X-Amz-Content-Sha256", payloadHash)
	if err := awsv4.NewSigner().SignHTTP(
		context.Background(),
		awsCreds(creds.ak, creds.sk, creds.st),
		sdkReq,
		payloadHash,
		"bedrock",
		"us-east-1",
		now,
	); err != nil {
		t.Fatal(err)
	}

	gotLight := normalizeAuthz(light.Header.Get("Authorization"))
	gotSDK := normalizeAuthz(sdkReq.Header.Get("Authorization"))
	if gotLight != gotSDK {
		t.Errorf("Authorization mismatch:\n light: %s\n sdk:   %s", gotLight, gotSDK)
	}
}

// normalizeAuthz strips spaces inside the comma-separated parameter list so
// minor whitespace differences don't fail the byte-comparison.
func normalizeAuthz(s string) string {
	s = strings.TrimSpace(s)
	parts := strings.SplitN(s, " ", 2)
	if len(parts) != 2 {
		return s
	}
	rest := strings.Split(parts[1], ",")
	for i, p := range rest {
		rest[i] = strings.TrimSpace(p)
	}
	return parts[0] + " " + strings.Join(rest, ", ")
}

// TestSigV4MatchesAWSSDK_InferenceProfileARN is a regression test for the
// signature mismatch hit when ModelID is an application-inference-profile ARN
// whose path contains a `/` (url.PathEscape -> %2F). The canonical URI must
// double-encode that to %252F, matching the AWS SDK; signing from u.Path
// instead of u.EscapedPath() produced a bare `/` and a 403.
func TestSigV4MatchesAWSSDK_InferenceProfileARN(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":[{"text":"hi"}]}]}`)
	const arn = "arn:aws:bedrock:us-east-1:130726505375:application-inference-profile/kzezlud25yzf"
	mkReq := func() *http.Request {
		u := "https://bedrock-runtime.us-east-1.amazonaws.com/model/" +
			url.PathEscape(arn) + "/converse-stream"
		r, _ := http.NewRequest("POST", u, io.NopCloser(bytes.NewReader(body)))
		r.Header.Set("Content-Type", "application/json")
		return r
	}
	now := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	ak, sk, st := "AKIDEXAMPLE", "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY", ""

	light := mkReq()
	if err := (&sigv4.Signer{Region: "us-east-1", Service: "bedrock"}).Sign(
		light, sigv4.Credentials{AccessKeyID: ak, SecretAccessKey: sk}, now,
	); err != nil {
		t.Fatal(err)
	}

	sdkReq := mkReq()
	sdkReq.Header.Set("Host", sdkReq.URL.Host)
	payloadHash := hexSHA256(body)
	sdkReq.Header.Set("X-Amz-Content-Sha256", payloadHash)
	if err := awsv4.NewSigner().SignHTTP(
		context.Background(), awsCreds(ak, sk, st), sdkReq,
		payloadHash, "bedrock", "us-east-1", now,
	); err != nil {
		t.Fatal(err)
	}

	gotLight := normalizeAuthz(light.Header.Get("Authorization"))
	gotSDK := normalizeAuthz(sdkReq.Header.Get("Authorization"))
	if gotLight != gotSDK {
		t.Errorf("Authorization mismatch:\n light: %s\n sdk:   %s", gotLight, gotSDK)
	}
}
