package creds

import (
	"bytes"
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

const stsAssumeRoleResp = `<AssumeRoleResponse>
  <AssumeRoleResult>
    <Credentials>
      <AccessKeyId>RAK</AccessKeyId>
      <SecretAccessKey>RSK</SecretAccessKey>
      <SessionToken>RST</SessionToken>
      <Expiration>2099-01-01T00:00:00Z</Expiration>
    </Credentials>
  </AssumeRoleResult>
</AssumeRoleResponse>`

func newSTSServer(t *testing.T, body string, status int, check func(form map[string][]string, r *http.Request)) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		if check != nil {
			check(r.Form, r)
		}
		if r.Header.Get("Authorization") == "" {
			t.Error("missing Authorization header (sigv4)")
		}
		if status != 0 {
			w.WriteHeader(status)
		}
		w.Write([]byte(body))
	}))
}

func TestAssumeRoleBasic(t *testing.T) {
	srv := newSTSServer(t, stsAssumeRoleResp, 0, func(f map[string][]string, r *http.Request) {
		if f["RoleArn"][0] != "arn:role" {
			t.Errorf("role=%v", f["RoleArn"])
		}
		if f["DurationSeconds"][0] != "1800" {
			t.Errorf("dur=%v", f["DurationSeconds"])
		}
		if f["ExternalId"][0] != "extX" {
			t.Errorf("ext=%v", f["ExternalId"])
		}
	})
	defer srv.Close()

	dir := t.TempDir()
	credPath := filepath.Join(dir, "credentials")
	must(os.WriteFile(credPath, []byte("[base]\naws_access_key_id=AK\naws_secret_access_key=SK\n"), 0o600))
	cfgPath := filepath.Join(dir, "config")
	must(os.WriteFile(cfgPath, []byte(`[profile target]
role_arn = arn:role
source_profile = base
duration_seconds = 1800
external_id = extX
role_session_name = mysession
region = eu-west-2
`), 0o600))

	c := DefaultChain(Config{
		Profile: "target", CredentialsFile: credPath, ConfigFile: cfgPath,
		Env: emptyEnv, STSEndpoint: srv.URL, HTTPClient: srv.Client(),
	})
	v, err := c.Retrieve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if v.AccessKeyID != "RAK" {
		t.Errorf("got %+v", v)
	}
}

func TestAssumeRoleMFA(t *testing.T) {
	srv := newSTSServer(t, stsAssumeRoleResp, 0, func(f map[string][]string, r *http.Request) {
		if f["SerialNumber"][0] != "arn:mfa" || f["TokenCode"][0] != "123456" {
			t.Errorf("mfa form=%v", f)
		}
	})
	defer srv.Close()

	dir := t.TempDir()
	credPath := filepath.Join(dir, "credentials")
	must(os.WriteFile(credPath, []byte("[base]\naws_access_key_id=AK\naws_secret_access_key=SK\n[target]\nrole_arn=arn:role\nsource_profile=base\nmfa_serial=arn:mfa\n"), 0o600))

	called := false
	c := DefaultChain(Config{
		Profile: "target", CredentialsFile: credPath, ConfigFile: filepath.Join(dir, "no"),
		Env:         emptyEnv,
		STSEndpoint: srv.URL, HTTPClient: srv.Client(),
		MFATokenProvider: func(serial string) (string, error) {
			called = true
			if serial != "arn:mfa" {
				t.Errorf("serial=%s", serial)
			}
			return "123456", nil
		},
	})
	if _, err := c.Retrieve(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Error("mfa not called")
	}
}

func TestAssumeRoleMFADefaultStdin(t *testing.T) {
	srv := newSTSServer(t, stsAssumeRoleResp, 0, nil)
	defer srv.Close()
	dir := t.TempDir()
	credPath := filepath.Join(dir, "credentials")
	must(os.WriteFile(credPath, []byte("[base]\naws_access_key_id=AK\naws_secret_access_key=SK\n[target]\nrole_arn=arn:role\nsource_profile=base\nmfa_serial=arn:mfa\n"), 0o600))

	stderr := &bytes.Buffer{}
	c := DefaultChain(Config{
		Profile: "target", CredentialsFile: credPath, ConfigFile: filepath.Join(dir, "no"),
		Env: emptyEnv, STSEndpoint: srv.URL, HTTPClient: srv.Client(),
		Stdin:  strings.NewReader("987654\n"),
		Stderr: stderr,
	})
	if _, err := c.Retrieve(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr.String(), "MFA code") {
		t.Errorf("stderr=%q", stderr.String())
	}
}

func TestAssumeRoleMFAEmptyToken(t *testing.T) {
	dir := t.TempDir()
	credPath := filepath.Join(dir, "credentials")
	must(os.WriteFile(credPath, []byte("[base]\naws_access_key_id=AK\naws_secret_access_key=SK\n[target]\nrole_arn=arn:role\nsource_profile=base\nmfa_serial=arn:mfa\n"), 0o600))
	c := DefaultChain(Config{
		Profile: "target", CredentialsFile: credPath, ConfigFile: filepath.Join(dir, "no"),
		Env:   emptyEnv,
		Stdin: strings.NewReader("\n"), Stderr: &bytes.Buffer{},
	})
	if _, err := c.Retrieve(context.Background()); err == nil {
		t.Error("want err")
	}
}

func TestAssumeRoleMFAProviderError(t *testing.T) {
	dir := t.TempDir()
	credPath := filepath.Join(dir, "credentials")
	must(os.WriteFile(credPath, []byte("[base]\naws_access_key_id=AK\naws_secret_access_key=SK\n[target]\nrole_arn=arn:role\nsource_profile=base\nmfa_serial=arn:mfa\n"), 0o600))
	c := DefaultChain(Config{
		Profile: "target", CredentialsFile: credPath, ConfigFile: filepath.Join(dir, "no"),
		Env:              emptyEnv,
		MFATokenProvider: func(string) (string, error) { return "", errors.New("nope") },
	})
	if _, err := c.Retrieve(context.Background()); err == nil {
		t.Error("want err")
	}
}

func TestAssumeRoleMissingSourceAndCredSrc(t *testing.T) {
	dir := t.TempDir()
	credPath := filepath.Join(dir, "credentials")
	must(os.WriteFile(credPath, []byte("[target]\nrole_arn=arn:role\n"), 0o600))
	c := DefaultChain(Config{
		Profile: "target", CredentialsFile: credPath, ConfigFile: filepath.Join(dir, "no"),
		Env: emptyEnv,
	})
	if _, err := c.Retrieve(context.Background()); err == nil {
		t.Error("want err")
	}
}

func TestAssumeRoleBothSourceAndCredSrc(t *testing.T) {
	dir := t.TempDir()
	credPath := filepath.Join(dir, "credentials")
	must(os.WriteFile(credPath, []byte("[base]\naws_access_key_id=AK\naws_secret_access_key=SK\n[target]\nrole_arn=arn:role\nsource_profile=base\ncredential_source=Environment\n"), 0o600))
	c := DefaultChain(Config{
		Profile: "target", CredentialsFile: credPath, ConfigFile: filepath.Join(dir, "no"),
		Env: emptyEnv,
	})
	if _, err := c.Retrieve(context.Background()); err == nil {
		t.Error("want err")
	}
}

func TestAssumeRoleSourceProfileCycle(t *testing.T) {
	dir := t.TempDir()
	credPath := filepath.Join(dir, "credentials")
	must(os.WriteFile(credPath, []byte("[a]\nrole_arn=arn:a\nsource_profile=b\n[b]\nrole_arn=arn:b\nsource_profile=a\n"), 0o600))
	c := DefaultChain(Config{
		Profile: "a", CredentialsFile: credPath, ConfigFile: filepath.Join(dir, "no"),
		Env: emptyEnv,
	})
	_, err := c.Retrieve(context.Background())
	if err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Errorf("got %v", err)
	}
}

func TestAssumeRoleCredentialSourceEnvironment(t *testing.T) {
	srv := newSTSServer(t, stsAssumeRoleResp, 0, nil)
	defer srv.Close()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config")
	must(os.WriteFile(cfgPath, []byte("[profile target]\nrole_arn=arn:r\ncredential_source=Environment\n"), 0o600))
	c := DefaultChain(Config{
		Profile: "target", ConfigFile: cfgPath, CredentialsFile: filepath.Join(dir, "no"),
		Env: envMap(map[string]string{
			"AWS_PROFILE":           "", // honor explicit Profile
			"AWS_ACCESS_KEY_ID":     "", // env path skipped (Profile path requires not having env creds; but envProvider will match — so we need different test)
			"AWS_SECRET_ACCESS_KEY": "",
		}),
		STSEndpoint: srv.URL, HTTPClient: srv.Client(),
	})
	// Since envProvider returns errNoMatch (no AK), DefaultChain proceeds to
	// shared profile, which uses credential_source=Environment. That tries
	// envProvider again which still errNoMatches — surfacing an error.
	if _, err := c.Retrieve(context.Background()); err == nil {
		t.Error("want err: env empty")
	}
}

func TestAssumeRoleCredentialSourceUnknown(t *testing.T) {
	dir := t.TempDir()
	credPath := filepath.Join(dir, "credentials")
	must(os.WriteFile(credPath, []byte("[t]\nrole_arn=arn\ncredential_source=Bogus\n"), 0o600))
	c := DefaultChain(Config{
		Profile: "t", CredentialsFile: credPath, ConfigFile: filepath.Join(dir, "no"),
		Env: emptyEnv,
	})
	if _, err := c.Retrieve(context.Background()); err == nil {
		t.Error("want err")
	}
}

func TestAssumeRoleDurationInvalid(t *testing.T) {
	dir := t.TempDir()
	credPath := filepath.Join(dir, "credentials")
	must(os.WriteFile(credPath, []byte("[base]\naws_access_key_id=A\naws_secret_access_key=B\n[t]\nrole_arn=arn\nsource_profile=base\nduration_seconds=NaN\n"), 0o600))
	c := DefaultChain(Config{
		Profile: "t", CredentialsFile: credPath, ConfigFile: filepath.Join(dir, "no"),
		Env: emptyEnv,
	})
	if _, err := c.Retrieve(context.Background()); err == nil {
		t.Error("want err")
	}
}

func TestAssumeRoleSTSError(t *testing.T) {
	srv := newSTSServer(t, `<ErrorResponse><Error><Code>AccessDenied</Code><Message>no</Message></Error></ErrorResponse>`, 403, nil)
	defer srv.Close()
	dir := t.TempDir()
	credPath := filepath.Join(dir, "credentials")
	must(os.WriteFile(credPath, []byte("[base]\naws_access_key_id=A\naws_secret_access_key=B\n[t]\nrole_arn=arn\nsource_profile=base\n"), 0o600))
	c := DefaultChain(Config{
		Profile: "t", CredentialsFile: credPath, ConfigFile: filepath.Join(dir, "no"),
		Env:         emptyEnv,
		STSEndpoint: srv.URL, HTTPClient: srv.Client(),
	})
	if _, err := c.Retrieve(context.Background()); err == nil {
		t.Error("want err")
	}
}

func TestAssumeRoleSTSDoError(t *testing.T) {
	dir := t.TempDir()
	credPath := filepath.Join(dir, "credentials")
	must(os.WriteFile(credPath, []byte("[base]\naws_access_key_id=A\naws_secret_access_key=B\n[t]\nrole_arn=arn\nsource_profile=base\n"), 0o600))
	c := DefaultChain(Config{
		Profile: "t", CredentialsFile: credPath, ConfigFile: filepath.Join(dir, "no"),
		Env: emptyEnv, STSEndpoint: "http://127.0.0.1:1",
		HTTPClient: &http.Client{Timeout: 200 * time.Millisecond},
	})
	if _, err := c.Retrieve(context.Background()); err == nil {
		t.Error("want err")
	}
}

func TestAssumeRoleBadXML(t *testing.T) {
	srv := newSTSServer(t, "<<<", 0, nil)
	defer srv.Close()
	dir := t.TempDir()
	credPath := filepath.Join(dir, "credentials")
	must(os.WriteFile(credPath, []byte("[base]\naws_access_key_id=A\naws_secret_access_key=B\n[t]\nrole_arn=arn\nsource_profile=base\n"), 0o600))
	c := DefaultChain(Config{
		Profile: "t", CredentialsFile: credPath, ConfigFile: filepath.Join(dir, "no"),
		Env: emptyEnv, STSEndpoint: srv.URL, HTTPClient: srv.Client(),
	})
	if _, err := c.Retrieve(context.Background()); err == nil {
		t.Error("want err")
	}
}

func TestSourceProfileChainOK(t *testing.T) {
	srv := newSTSServer(t, stsAssumeRoleResp, 0, nil)
	defer srv.Close()
	dir := t.TempDir()
	credPath := filepath.Join(dir, "credentials")
	// a -> assumes role using b -> assumes role using base (static).
	must(os.WriteFile(credPath, []byte(`[base]
aws_access_key_id = AK
aws_secret_access_key = SK
[b]
role_arn = arn:b
source_profile = base
[a]
role_arn = arn:a
source_profile = b
`), 0o600))
	c := DefaultChain(Config{
		Profile: "a", CredentialsFile: credPath, ConfigFile: filepath.Join(dir, "no"),
		Env: emptyEnv, STSEndpoint: srv.URL, HTTPClient: srv.Client(),
	})
	if _, err := c.Retrieve(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestProfileAbsentFallsThroughToIMDS(t *testing.T) {
	// No profile section -> errNoMatch -> falls into IMDS, which fails -> chain error.
	dir := t.TempDir()
	c := DefaultChain(Config{
		Profile:         "missing",
		CredentialsFile: filepath.Join(dir, "no"),
		ConfigFile:      filepath.Join(dir, "no"),
		Env:             envMap(map[string]string{"AWS_EC2_METADATA_DISABLED": "true"}),
	})
	_, err := c.Retrieve(context.Background())
	if err == nil || !strings.Contains(err.Error(), "no credentials") {
		t.Errorf("got %v", err)
	}
}

func TestResolveRegionEnvFallback(t *testing.T) {
	cfg := &Config{Env: envMap(map[string]string{"AWS_DEFAULT_REGION": "ap-south-1"})}
	if r := resolveRegion(cfg, ""); r != "ap-south-1" {
		t.Errorf("got %s", r)
	}
	cfg2 := &Config{Env: envMap(map[string]string{"AWS_REGION": "eu-1"})}
	if r := resolveRegion(cfg2, ""); r != "eu-1" {
		t.Errorf("got %s", r)
	}
	cfg3 := &Config{Region: "x", Env: emptyEnv}
	if r := resolveRegion(cfg3, "y"); r != "x" {
		t.Errorf("got %s", r)
	}
	cfg4 := &Config{Env: emptyEnv}
	if r := resolveRegion(cfg4, "p"); r != "p" {
		t.Errorf("got %s", r)
	}
	if r := resolveRegion(cfg4, ""); r != "us-east-1" {
		t.Errorf("got %s", r)
	}
}

func TestEnvProviderMissingSecret(t *testing.T) {
	_, err := envProvider(&Config{Env: envMap(map[string]string{"AWS_ACCESS_KEY_ID": "x"})})
	if err == nil {
		t.Error("want err")
	}
}

func TestDefaultHTTPClient(t *testing.T) {
	cfg := &Config{}
	if cfg.httpClient() == nil {
		t.Error("nil client")
	}
}

func TestSTSEndpointDefault(t *testing.T) {
	cfg := &Config{}
	if e := stsEndpoint(cfg, "us-west-2"); e != "https://sts.us-west-2.amazonaws.com/" {
		t.Errorf("got %s", e)
	}
}
