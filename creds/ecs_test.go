package creds

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestECSNoMatch(t *testing.T) {
	cfg := &Config{Env: emptyEnv}
	if _, err := ecsProvider(context.Background(), cfg); !errors.Is(err, errNoMatch) {
		t.Errorf("got %v", err)
	}
}

func newECSServer(t *testing.T, body string, status int, wantAuth string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if wantAuth != "" && r.Header.Get("Authorization") != wantAuth {
			t.Errorf("auth=%q want %q", r.Header.Get("Authorization"), wantAuth)
		}
		if status != 0 {
			w.WriteHeader(status)
		}
		w.Write([]byte(body))
	}))
}

func TestECSFullURISuccess(t *testing.T) {
	body := `{"AccessKeyId":"AK","SecretAccessKey":"SK","Token":"ST","Expiration":"2099-01-01T00:00:00Z"}`
	srv := newECSServer(t, body, 0, "")
	defer srv.Close()
	cfg := &Config{Env: envMap(map[string]string{
		"AWS_CONTAINER_CREDENTIALS_FULL_URI": srv.URL,
	}), HTTPClient: srv.Client()}
	v, err := ecsProvider(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if v.AccessKeyID != "AK" || v.SessionToken != "ST" || v.Expires.IsZero() {
		t.Errorf("%+v", v)
	}
}

func TestECSRelativeURIWithAuthToken(t *testing.T) {
	body := `{"AccessKeyId":"AK","SecretAccessKey":"SK"}`
	srv := newECSServer(t, body, 0, "Bearer abc")
	defer srv.Close()
	cfg := &Config{
		Env: envMap(map[string]string{
			"AWS_CONTAINER_CREDENTIALS_RELATIVE_URI": "/v2/creds/x",
			"AWS_CONTAINER_AUTHORIZATION_TOKEN":      "Bearer abc",
		}),
		ECSEndpoint: srv.URL,
		HTTPClient:  srv.Client(),
	}
	if _, err := ecsProvider(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
}

func TestECSAuthTokenFile(t *testing.T) {
	dir := t.TempDir()
	tokenFile := filepath.Join(dir, "tok")
	must(os.WriteFile(tokenFile, []byte(" Bearer xyz \n"), 0o600))
	srv := newECSServer(t, `{"AccessKeyId":"AK","SecretAccessKey":"SK"}`, 0, "Bearer xyz")
	defer srv.Close()
	cfg := &Config{
		Env: envMap(map[string]string{
			"AWS_CONTAINER_CREDENTIALS_FULL_URI":      srv.URL,
			"AWS_CONTAINER_AUTHORIZATION_TOKEN_FILE": tokenFile,
		}),
		HTTPClient: srv.Client(),
	}
	if _, err := ecsProvider(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
}

func TestECSAuthTokenFileMissing(t *testing.T) {
	cfg := &Config{Env: envMap(map[string]string{
		"AWS_CONTAINER_CREDENTIALS_FULL_URI":      "http://x",
		"AWS_CONTAINER_AUTHORIZATION_TOKEN_FILE": "/no/such/file",
	})}
	if _, err := ecsProvider(context.Background(), cfg); err == nil {
		t.Error("want err")
	}
}

func TestECSDoError(t *testing.T) {
	cfg := &Config{
		Env:        envMap(map[string]string{"AWS_CONTAINER_CREDENTIALS_FULL_URI": "http://127.0.0.1:1"}),
		HTTPClient: &http.Client{},
	}
	if _, err := ecsProvider(context.Background(), cfg); err == nil {
		t.Error("want err")
	}
}

func TestECSHTTPError(t *testing.T) {
	srv := newECSServer(t, "boom", 500, "")
	defer srv.Close()
	cfg := &Config{Env: envMap(map[string]string{"AWS_CONTAINER_CREDENTIALS_FULL_URI": srv.URL}), HTTPClient: srv.Client()}
	if _, err := ecsProvider(context.Background(), cfg); err == nil {
		t.Error("want err")
	}
}

func TestECSBadJSON(t *testing.T) {
	srv := newECSServer(t, "not json", 0, "")
	defer srv.Close()
	cfg := &Config{Env: envMap(map[string]string{"AWS_CONTAINER_CREDENTIALS_FULL_URI": srv.URL}), HTTPClient: srv.Client()}
	if _, err := ecsProvider(context.Background(), cfg); err == nil {
		t.Error("want err")
	}
}

func TestECSIncomplete(t *testing.T) {
	srv := newECSServer(t, `{}`, 0, "")
	defer srv.Close()
	cfg := &Config{Env: envMap(map[string]string{"AWS_CONTAINER_CREDENTIALS_FULL_URI": srv.URL}), HTTPClient: srv.Client()}
	if _, err := ecsProvider(context.Background(), cfg); err == nil {
		t.Error("want err")
	}
}

func TestECSBadExpiration(t *testing.T) {
	srv := newECSServer(t, `{"AccessKeyId":"a","SecretAccessKey":"b","Expiration":"x"}`, 0, "")
	defer srv.Close()
	cfg := &Config{Env: envMap(map[string]string{"AWS_CONTAINER_CREDENTIALS_FULL_URI": srv.URL}), HTTPClient: srv.Client()}
	if _, err := ecsProvider(context.Background(), cfg); err == nil {
		t.Error("want err")
	}
}

func TestECSBadRequest(t *testing.T) {
	// Force NewRequestWithContext to fail via invalid method (not possible with GET).
	// Instead force url parse failure with a bad URL char in FULL_URI.
	cfg := &Config{Env: envMap(map[string]string{"AWS_CONTAINER_CREDENTIALS_FULL_URI": "http://[::1"})}
	if _, err := ecsProvider(context.Background(), cfg); err == nil {
		t.Error("want err")
	}
}
