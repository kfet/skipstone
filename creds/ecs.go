package creds

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

const ecsDefaultEndpoint = "http://169.254.170.2"

// ecsProvider fetches credentials from the ECS task / Fargate creds endpoint.
// Returns errNoMatch if neither relevant env var is set.
func ecsProvider(ctx context.Context, cfg *Config) (Value, error) {
	rel := cfg.env("AWS_CONTAINER_CREDENTIALS_RELATIVE_URI")
	full := cfg.env("AWS_CONTAINER_CREDENTIALS_FULL_URI")
	if rel == "" && full == "" {
		return Value{}, errNoMatch
	}
	var url string
	if rel != "" {
		base := cfg.ECSEndpoint
		if base == "" {
			base = ecsDefaultEndpoint
		}
		url = base + rel
	} else {
		url = full
	}

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return Value{}, fmt.Errorf("creds: ecs req: %w", err)
	} // Authorization header — token literal or token file.
	if tok := cfg.env("AWS_CONTAINER_AUTHORIZATION_TOKEN"); tok != "" {
		req.Header.Set("Authorization", tok)
	} else if file := cfg.env("AWS_CONTAINER_AUTHORIZATION_TOKEN_FILE"); file != "" {
		data, err := os.ReadFile(file)
		if err != nil {
			return Value{}, fmt.Errorf("creds: ecs auth token file: %w", err)
		}
		req.Header.Set("Authorization", strings.TrimSpace(string(data)))
	}

	resp, err := cfg.httpClient().Do(req)
	if err != nil {
		return Value{}, fmt.Errorf("creds: ecs get: %w", err)
	}
	body := readAndClose(resp)
	if resp.StatusCode/100 != 2 {
		return Value{}, fmt.Errorf("creds: ecs HTTP %d: %s", resp.StatusCode, string(body))
	}
	var p struct {
		AccessKeyID     string `json:"AccessKeyId"`
		SecretAccessKey string `json:"SecretAccessKey"`
		Token           string `json:"Token"`
		Expiration      string `json:"Expiration"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return Value{}, fmt.Errorf("creds: ecs parse: %w", err)
	}
	if p.AccessKeyID == "" || p.SecretAccessKey == "" {
		return Value{}, errors.New("creds: ecs response missing access key or secret")
	}
	v := Value{
		AccessKeyID:     p.AccessKeyID,
		SecretAccessKey: p.SecretAccessKey,
		SessionToken:    p.Token,
	}
	if p.Expiration != "" {
		t, err := time.Parse(time.RFC3339, p.Expiration)
		if err != nil {
			return Value{}, fmt.Errorf("creds: ecs Expiration: %w", err)
		}
		v.Expires = t
	}
	return v, nil
}
