package creds

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWebIdentityNoMatch(t *testing.T) {
	cfg := &Config{Env: emptyEnv}
	if _, err := webIdentityProvider(context.Background(), cfg); !errors.Is(err, errNoMatch) {
		t.Errorf("got %v", err)
	}
}

func TestWebIdentityTokenFileMissing(t *testing.T) {
	cfg := &Config{Env: envMap(map[string]string{
		"AWS_WEB_IDENTITY_TOKEN_FILE": "/no/such",
		"AWS_ROLE_ARN":                "arn:aws:iam::1:role/r",
	})}
	if _, err := webIdentityProvider(context.Background(), cfg); err == nil {
		t.Error("want err")
	}
}

func writeFile(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "f")
	must(os.WriteFile(p, []byte(content), 0o600))
	return p
}

const stsOKResp = `<AssumeRoleWithWebIdentityResponse>
  <AssumeRoleWithWebIdentityResult>
    <Credentials>
      <AccessKeyId>AK</AccessKeyId>
      <SecretAccessKey>SK</SecretAccessKey>
      <SessionToken>ST</SessionToken>
      <Expiration>2099-01-01T00:00:00Z</Expiration>
    </Credentials>
  </AssumeRoleWithWebIdentityResult>
</AssumeRoleWithWebIdentityResponse>`

func TestWebIdentitySuccess(t *testing.T) {
	tokenFile := writeFile(t, "thetoken")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		if r.Form.Get("Action") != "AssumeRoleWithWebIdentity" {
			t.Errorf("action=%s", r.Form.Get("Action"))
		}
		if r.Form.Get("WebIdentityToken") != "thetoken" {
			t.Errorf("token=%s", r.Form.Get("WebIdentityToken"))
		}
		if !strings.HasPrefix(r.Form.Get("RoleSessionName"), "session-") &&
			!strings.HasPrefix(r.Form.Get("RoleSessionName"), "bedrock-light-") {
			t.Errorf("session name=%s", r.Form.Get("RoleSessionName"))
		}
		w.Write([]byte(stsOKResp))
	}))
	defer srv.Close()
	cfg := &Config{
		Env: envMap(map[string]string{
			"AWS_WEB_IDENTITY_TOKEN_FILE": tokenFile,
			"AWS_ROLE_ARN":                "arn",
		}),
		STSEndpoint: srv.URL,
		HTTPClient:  srv.Client(),
		Now:         func() time.Time { return time.Unix(123, 0) },
	}
	v, err := webIdentityProvider(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if v.AccessKeyID != "AK" || v.SessionToken != "ST" || v.Expires.IsZero() {
		t.Errorf("got %+v", v)
	}
}

func TestWebIdentityCustomSessionName(t *testing.T) {
	tokenFile := writeFile(t, "tok")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		if r.Form.Get("RoleSessionName") != "myname" {
			t.Errorf("session=%s", r.Form.Get("RoleSessionName"))
		}
		w.Write([]byte(stsOKResp))
	}))
	defer srv.Close()
	cfg := &Config{
		Env: envMap(map[string]string{
			"AWS_WEB_IDENTITY_TOKEN_FILE": tokenFile,
			"AWS_ROLE_ARN":                "arn",
			"AWS_ROLE_SESSION_NAME":       "myname",
		}),
		STSEndpoint: srv.URL,
		HTTPClient:  srv.Client(),
	}
	if _, err := webIdentityProvider(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
}

func TestWebIdentityHTTPErrorXML(t *testing.T) {
	tokenFile := writeFile(t, "t")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
		w.Write([]byte(`<ErrorResponse><Error><Code>AccessDenied</Code><Message>nope</Message></Error></ErrorResponse>`))
	}))
	defer srv.Close()
	cfg := &Config{
		Env: envMap(map[string]string{
			"AWS_WEB_IDENTITY_TOKEN_FILE": tokenFile,
			"AWS_ROLE_ARN":                "arn",
		}),
		STSEndpoint: srv.URL, HTTPClient: srv.Client(),
	}
	_, err := webIdentityProvider(context.Background(), cfg)
	if err == nil || !strings.Contains(err.Error(), "AccessDenied") {
		t.Errorf("got %v", err)
	}
}

func TestWebIdentityHTTPErrorPlain(t *testing.T) {
	tokenFile := writeFile(t, "t")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		w.Write([]byte(`oops`))
	}))
	defer srv.Close()
	cfg := &Config{
		Env:         envMap(map[string]string{"AWS_WEB_IDENTITY_TOKEN_FILE": tokenFile, "AWS_ROLE_ARN": "arn"}),
		STSEndpoint: srv.URL, HTTPClient: srv.Client(),
	}
	if _, err := webIdentityProvider(context.Background(), cfg); err == nil {
		t.Error("want err")
	}
}

func TestWebIdentityBadXML(t *testing.T) {
	tokenFile := writeFile(t, "t")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<<<not xml`))
	}))
	defer srv.Close()
	cfg := &Config{
		Env:         envMap(map[string]string{"AWS_WEB_IDENTITY_TOKEN_FILE": tokenFile, "AWS_ROLE_ARN": "arn"}),
		STSEndpoint: srv.URL, HTTPClient: srv.Client(),
	}
	if _, err := webIdentityProvider(context.Background(), cfg); err == nil {
		t.Error("want err")
	}
}

func TestWebIdentityEmptyCreds(t *testing.T) {
	tokenFile := writeFile(t, "t")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<AssumeRoleWithWebIdentityResponse><AssumeRoleWithWebIdentityResult><Credentials></Credentials></AssumeRoleWithWebIdentityResult></AssumeRoleWithWebIdentityResponse>`))
	}))
	defer srv.Close()
	cfg := &Config{
		Env:         envMap(map[string]string{"AWS_WEB_IDENTITY_TOKEN_FILE": tokenFile, "AWS_ROLE_ARN": "arn"}),
		STSEndpoint: srv.URL, HTTPClient: srv.Client(),
	}
	if _, err := webIdentityProvider(context.Background(), cfg); err == nil {
		t.Error("want err")
	}
}

func TestWebIdentityBadExpiration(t *testing.T) {
	tokenFile := writeFile(t, "t")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<AssumeRoleWithWebIdentityResponse><AssumeRoleWithWebIdentityResult><Credentials><AccessKeyId>a</AccessKeyId><SecretAccessKey>b</SecretAccessKey><Expiration>nope</Expiration></Credentials></AssumeRoleWithWebIdentityResult></AssumeRoleWithWebIdentityResponse>`))
	}))
	defer srv.Close()
	cfg := &Config{
		Env:         envMap(map[string]string{"AWS_WEB_IDENTITY_TOKEN_FILE": tokenFile, "AWS_ROLE_ARN": "arn"}),
		STSEndpoint: srv.URL, HTTPClient: srv.Client(),
	}
	if _, err := webIdentityProvider(context.Background(), cfg); err == nil {
		t.Error("want err")
	}
}

func TestWebIdentityHTTPDoError(t *testing.T) {
	tokenFile := writeFile(t, "t")
	cfg := &Config{
		Env:         envMap(map[string]string{"AWS_WEB_IDENTITY_TOKEN_FILE": tokenFile, "AWS_ROLE_ARN": "arn"}),
		STSEndpoint: "http://127.0.0.1:1",
		HTTPClient:  &http.Client{Timeout: 200 * time.Millisecond},
	}
	if _, err := webIdentityProvider(context.Background(), cfg); err == nil {
		t.Error("want err")
	}
}
