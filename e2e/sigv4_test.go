// SigV4 cross-check: sign the same request with skipstone and the AWS SDK
// SigV4 signer; the Authorization headers must match byte-for-byte.
package e2e

import (
	"bytes"
	"context"
	"io"
	"net/http"
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
