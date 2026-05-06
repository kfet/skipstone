package creds

import (
	"context"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// webIdentityProvider implements AssumeRoleWithWebIdentity (IRSA).
// Returns errNoMatch unless both AWS_WEB_IDENTITY_TOKEN_FILE and AWS_ROLE_ARN are set.
func webIdentityProvider(ctx context.Context, cfg *Config) (Value, error) {
	tokenFile := cfg.env("AWS_WEB_IDENTITY_TOKEN_FILE")
	roleArn := cfg.env("AWS_ROLE_ARN")
	if tokenFile == "" || roleArn == "" {
		return Value{}, errNoMatch
	}
	tokenBytes, err := os.ReadFile(tokenFile)
	if err != nil {
		return Value{}, fmt.Errorf("creds: read web identity token: %w", err)
	}
	token := strings.TrimSpace(string(tokenBytes))
	sessionName := cfg.env("AWS_ROLE_SESSION_NAME")
	if sessionName == "" {
		sessionName = "bedrock-light-" + strconv.FormatInt(cfg.now().UnixNano(), 10)
	}

	region := resolveRegion(cfg, "")
	endpoint := stsEndpoint(cfg, region)

	form := url.Values{}
	form.Set("Action", "AssumeRoleWithWebIdentity")
	form.Set("Version", "2011-06-15")
	form.Set("RoleArn", roleArn)
	form.Set("RoleSessionName", sessionName)
	form.Set("WebIdentityToken", token)

	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/xml")

	resp, err := cfg.httpClient().Do(req)
	if err != nil {
		return Value{}, fmt.Errorf("creds: irsa do: %w", err)
	}
	body := readAndClose(resp)
	if resp.StatusCode/100 != 2 {
		return Value{}, parseSTSError(body, resp.StatusCode)
	}
	var out struct {
		XMLName xml.Name `xml:"AssumeRoleWithWebIdentityResponse"`
		Result  struct {
			Credentials stsCredentialsXML `xml:"Credentials"`
		} `xml:"AssumeRoleWithWebIdentityResult"`
	}
	if err := xml.Unmarshal(body, &out); err != nil {
		return Value{}, fmt.Errorf("creds: irsa xml: %w", err)
	}
	return out.Result.Credentials.toValue("irsa")
}

type stsCredentialsXML struct {
	AccessKeyID     string `xml:"AccessKeyId"`
	SecretAccessKey string `xml:"SecretAccessKey"`
	SessionToken    string `xml:"SessionToken"`
	Expiration      string `xml:"Expiration"`
}

func (c stsCredentialsXML) toValue(tag string) (Value, error) {
	if c.AccessKeyID == "" || c.SecretAccessKey == "" {
		return Value{}, fmt.Errorf("creds: %s: empty credentials in response", tag)
	}
	v := Value{
		AccessKeyID:     c.AccessKeyID,
		SecretAccessKey: c.SecretAccessKey,
		SessionToken:    c.SessionToken,
	}
	if c.Expiration != "" {
		t, err := time.Parse(time.RFC3339, c.Expiration)
		if err != nil {
			return Value{}, fmt.Errorf("creds: %s Expiration: %w", tag, err)
		}
		v.Expires = t
	}
	return v, nil
}

func stsEndpoint(cfg *Config, region string) string {
	if cfg.STSEndpoint != "" {
		return cfg.STSEndpoint
	}
	return "https://sts." + region + ".amazonaws.com/"
}

// parseSTSError extracts the AWS XML error envelope into a useful Go error.
func parseSTSError(body []byte, status int) error {
	var e struct {
		XMLName xml.Name `xml:"ErrorResponse"`
		Error   struct {
			Code    string `xml:"Code"`
			Message string `xml:"Message"`
		} `xml:"Error"`
	}
	if err := xml.Unmarshal(body, &e); err == nil && e.Error.Code != "" {
		return fmt.Errorf("creds: sts HTTP %d %s: %s", status, e.Error.Code, e.Error.Message)
	}
	return fmt.Errorf("creds: sts HTTP %d: %s", status, string(body))
}
