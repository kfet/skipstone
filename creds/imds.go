package creds

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	imdsDefaultEndpoint = "http://169.254.169.254"
	imdsTokenTTL        = "21600" // 6h, AWS recommended
	imdsTimeout         = time.Second
)

// imdsProvider fetches credentials from EC2 IMDSv2.
// Returns errNoMatch if AWS_EC2_METADATA_DISABLED=true or the metadata service
// is unreachable / has no role bound.
func imdsProvider(ctx context.Context, cfg *Config) (Value, error) {
	if strings.EqualFold(cfg.env("AWS_EC2_METADATA_DISABLED"), "true") {
		return Value{}, errNoMatch
	}
	endpoint := cfg.IMDSEndpoint
	if endpoint == "" {
		endpoint = imdsDefaultEndpoint
	}

	hc := cfg.httpClient()
	tctx, cancel := context.WithTimeout(ctx, imdsTimeout)
	defer cancel()

	// 1. Fetch session token.
	tokReq, _ := http.NewRequestWithContext(tctx, http.MethodPut, endpoint+"/latest/api/token", nil)
	tokReq.Header.Set("X-aws-ec2-metadata-token-ttl-seconds", imdsTokenTTL)
	tokResp, err := hc.Do(tokReq)
	if err != nil {
		return Value{}, errNoMatch // not on EC2
	}
	tokBody := readAndClose(tokResp)
	if tokResp.StatusCode/100 != 2 {
		return Value{}, fmt.Errorf("creds: imds token: HTTP %d", tokResp.StatusCode)
	}
	token := strings.TrimSpace(string(tokBody))

	// 2. Discover the role name.
	role, err := imdsGet(tctx, hc, endpoint+"/latest/meta-data/iam/security-credentials/", token)
	if err != nil {
		return Value{}, err
	}
	role = strings.TrimSpace(role)
	if role == "" {
		return Value{}, errors.New("creds: imds: no role bound to instance")
	}

	// 3. Fetch the credentials for that role.
	body, err := imdsGet(tctx, hc, endpoint+"/latest/meta-data/iam/security-credentials/"+role, token)
	if err != nil {
		return Value{}, err
	}
	var p struct {
		AccessKeyID     string `json:"AccessKeyId"`
		SecretAccessKey string `json:"SecretAccessKey"`
		Token           string `json:"Token"`
		Expiration      string `json:"Expiration"`
		Code            string `json:"Code"`
	}
	if err := json.Unmarshal([]byte(body), &p); err != nil {
		return Value{}, fmt.Errorf("creds: imds creds parse: %w", err)
	}
	if p.Code != "" && p.Code != "Success" {
		return Value{}, fmt.Errorf("creds: imds creds Code=%q", p.Code)
	}
	if p.AccessKeyID == "" || p.SecretAccessKey == "" {
		return Value{}, errors.New("creds: imds creds incomplete")
	}
	v := Value{
		AccessKeyID:     p.AccessKeyID,
		SecretAccessKey: p.SecretAccessKey,
		SessionToken:    p.Token,
	}
	if p.Expiration != "" {
		t, err := time.Parse(time.RFC3339, p.Expiration)
		if err != nil {
			return Value{}, fmt.Errorf("creds: imds Expiration: %w", err)
		}
		v.Expires = t
	}
	return v, nil
}

func imdsGet(ctx context.Context, hc *http.Client, url, token string) (string, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	req.Header.Set("X-aws-ec2-metadata-token", token)
	resp, err := hc.Do(req)
	if err != nil {
		return "", fmt.Errorf("creds: imds get: %w", err)
	}
	body := readAndClose(resp)
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("creds: imds %s: HTTP %d", url, resp.StatusCode)
	}
	return string(body), nil
}

func readAndClose(resp *http.Response) []byte {
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return b
}
