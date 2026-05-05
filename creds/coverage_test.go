package creds

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// rtFunc adapts a function to http.RoundTripper.
type rtFunc func(*http.Request) (*http.Response, error)

func (f rtFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// errReader returns a non-EOF error on Read.
type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("read fail") }
func (errReader) Close() error             { return nil }

// ---- Coverage tests for resolveCredentialSource branches ----

func TestCredentialSourceEcsContainerNoMatch(t *testing.T) {
	dir := t.TempDir()
	credPath := filepath.Join(dir, "credentials")
	must(os.WriteFile(credPath, []byte("[t]\nrole_arn=arn\ncredential_source=EcsContainer\n"), 0o600))
	c := DefaultChain(Config{
		Profile: "t", CredentialsFile: credPath, ConfigFile: filepath.Join(dir, "no"),
		Env: emptyEnv,
	})
	if _, err := c.Retrieve(context.Background()); err == nil {
		t.Error("want err")
	}
}

func TestCredentialSourceEc2InstanceMetadataNoMatch(t *testing.T) {
	dir := t.TempDir()
	credPath := filepath.Join(dir, "credentials")
	must(os.WriteFile(credPath, []byte("[t]\nrole_arn=arn\ncredential_source=Ec2InstanceMetadata\n"), 0o600))
	c := DefaultChain(Config{
		Profile: "t", CredentialsFile: credPath, ConfigFile: filepath.Join(dir, "no"),
		Env: envMap(map[string]string{"AWS_EC2_METADATA_DISABLED": "true"}),
	})
	if _, err := c.Retrieve(context.Background()); err == nil {
		t.Error("want err")
	}
}

func TestCredentialSourceEnvironmentSuccess(t *testing.T) {
	cfg := &Config{Env: envMap(map[string]string{
		"AWS_ACCESS_KEY_ID": "AK", "AWS_SECRET_ACCESS_KEY": "SK",
	})}
	v, err := resolveCredentialSource(context.Background(), cfg, "Environment")
	if err != nil || v.AccessKeyID != "AK" {
		t.Errorf("got %+v err=%v", v, err)
	}
}

func TestCredentialSourceEcsContainerSuccess(t *testing.T) {
	ecsSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"AccessKeyId":"SRC","SecretAccessKey":"SK"}`))
	}))
	defer ecsSrv.Close()
	cfg := &Config{
		Env: envMap(map[string]string{
			"AWS_CONTAINER_CREDENTIALS_FULL_URI": ecsSrv.URL,
		}),
		HTTPClient: ecsSrv.Client(),
	}
	v, err := resolveCredentialSource(context.Background(), cfg, "EcsContainer")
	if err != nil {
		t.Fatal(err)
	}
	if v.AccessKeyID != "SRC" {
		t.Errorf("got %+v", v)
	}
}

func TestCredentialSourceEc2Success(t *testing.T) {
	imdsSrv := newIMDSServer(t, "r", `{"AccessKeyId":"SRC","SecretAccessKey":"SK","Code":"Success"}`, 0, 0, 0)
	defer imdsSrv.Close()
	cfg := &Config{
		Env: emptyEnv, HTTPClient: imdsSrv.Client(), IMDSEndpoint: imdsSrv.URL,
	}
	v, err := resolveCredentialSource(context.Background(), cfg, "Ec2InstanceMetadata")
	if err != nil {
		t.Fatal(err)
	}
	if v.AccessKeyID != "SRC" {
		t.Errorf("got %+v", v)
	}
}

// ---- mfaToken default Stdin/Stderr (uses real os.Stdin/os.Stderr) ----

func TestMFADefaultIO(t *testing.T) {
	// Force Stdin/Stderr defaults by leaving them nil. os.Stdin in test = no input
	// → ReadString returns EOF → empty token → error.
	cfg := &Config{}
	if _, err := mfaToken(cfg, "arn:mfa"); err == nil {
		t.Error("want empty-token err")
	}
}

func TestMFAReadError(t *testing.T) {
	cfg := &Config{Stdin: errReader{}, Stderr: &bytes.Buffer{}}
	if _, err := mfaToken(cfg, "arn:mfa"); err == nil {
		t.Error("want read err")
	}
}

// ---- resolveProfile "no creds" path ----

func TestResolveProfileNoCredsButSectionExists(t *testing.T) {
	dir := t.TempDir()
	credPath := filepath.Join(dir, "credentials")
	must(os.WriteFile(credPath, []byte("[p]\nregion=us-west-2\n"), 0o600))
	c := DefaultChain(Config{
		Profile: "p", CredentialsFile: credPath, ConfigFile: filepath.Join(dir, "no"),
		Env: emptyEnv,
	})
	_, err := c.Retrieve(context.Background())
	if err == nil || !strings.Contains(err.Error(), `no credentials found for profile "p"`) {
		t.Errorf("got %v", err)
	}
}

// ---- ECS default endpoint via custom transport ----

func TestECSRelativeURIDefaultEndpoint(t *testing.T) {
	called := false
	cfg := &Config{
		Env: envMap(map[string]string{
			"AWS_CONTAINER_CREDENTIALS_RELATIVE_URI": "/v2/x",
		}),
		HTTPClient: &http.Client{Transport: rtFunc(func(r *http.Request) (*http.Response, error) {
			called = true
			if r.URL.Host != "169.254.170.2" {
				t.Errorf("host=%s", r.URL.Host)
			}
			return &http.Response{
				StatusCode: 200,
				Body:       io.NopCloser(strings.NewReader(`{"AccessKeyId":"a","SecretAccessKey":"b"}`)),
			}, nil
		})},
	}
	if _, err := ecsProvider(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Error("transport not used")
	}
}

// ---- IMDS imdsGet error path: token call OK, role call fails ----

func TestIMDSRoleFetchTransportError(t *testing.T) {
	cfg := &Config{
		Env:          emptyEnv,
		IMDSEndpoint: "http://imds",
		HTTPClient: &http.Client{Transport: rtFunc(func(r *http.Request) (*http.Response, error) {
			if r.URL.Path == "/latest/api/token" {
				return &http.Response{
					StatusCode: 200,
					Body:       io.NopCloser(strings.NewReader("TOK")),
				}, nil
			}
			return nil, errors.New("boom")
		})},
	}
	if _, err := imdsProvider(context.Background(), cfg); err == nil {
		t.Error("want err")
	}
}
