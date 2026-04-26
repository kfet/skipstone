package creds

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func emptyEnv(string) string { return "" }

func envMap(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestStatic(t *testing.T) {
	p := Static("a", "b", "c")
	v, err := p.Retrieve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if v.AccessKeyID != "a" || v.SecretAccessKey != "b" || v.SessionToken != "c" {
		t.Errorf("static creds wrong: %+v", v)
	}
}

func TestProviderFunc(t *testing.T) {
	want := errors.New("boom")
	pf := ProviderFunc(func(context.Context) (Value, error) { return Value{}, want })
	if _, err := pf.Retrieve(context.Background()); err != want {
		t.Errorf("got %v, want %v", err, want)
	}
}

func TestEnvCreds(t *testing.T) {
	c := DefaultChain(Config{Env: envMap(map[string]string{
		"AWS_ACCESS_KEY_ID":     "AK",
		"AWS_SECRET_ACCESS_KEY": "SK",
		"AWS_SESSION_TOKEN":     "ST",
	})})
	v, err := c.Retrieve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if v.AccessKeyID != "AK" || v.SecretAccessKey != "SK" || v.SessionToken != "ST" {
		t.Errorf("env creds: %+v", v)
	}
	// Cached on second call.
	v2, _ := c.Retrieve(context.Background())
	if v2 != v {
		t.Error("expected cached value")
	}
}

func TestEnvMissingSecret(t *testing.T) {
	c := DefaultChain(Config{Env: envMap(map[string]string{"AWS_ACCESS_KEY_ID": "AK"})})
	if _, err := c.Retrieve(context.Background()); err == nil {
		t.Error("expected error")
	}
}

func TestProfileStaticFromCredentials(t *testing.T) {
	dir := t.TempDir()
	credPath := filepath.Join(dir, "credentials")
	must(os.WriteFile(credPath, []byte("[work]\naws_access_key_id=AKW\naws_secret_access_key=SKW\naws_session_token=TOK\n"), 0o600))
	c := DefaultChain(Config{
		Profile:         "work",
		CredentialsFile: credPath,
		ConfigFile:      filepath.Join(dir, "config-missing"),
		Env:             emptyEnv,
	})
	v, err := c.Retrieve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if v.AccessKeyID != "AKW" || v.SessionToken != "TOK" {
		t.Errorf("got %+v", v)
	}
}

func TestProfileFromEnvFallback(t *testing.T) {
	dir := t.TempDir()
	credPath := filepath.Join(dir, "credentials")
	must(os.WriteFile(credPath, []byte("[default]\naws_access_key_id=A\naws_secret_access_key=B\n"), 0o600))
	c := DefaultChain(Config{
		CredentialsFile: credPath,
		ConfigFile:      filepath.Join(dir, "no"),
		Env:             envMap(map[string]string{"AWS_PROFILE": ""}), // empty -> default
	})
	if _, err := c.Retrieve(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestProfileFromAwsProfileEnv(t *testing.T) {
	dir := t.TempDir()
	credPath := filepath.Join(dir, "credentials")
	must(os.WriteFile(credPath, []byte("[work]\naws_access_key_id=AKW\naws_secret_access_key=SKW\n"), 0o600))
	c := DefaultChain(Config{
		CredentialsFile: credPath,
		ConfigFile:      filepath.Join(dir, "no"),
		Env:             envMap(map[string]string{"AWS_PROFILE": "work"}),
	})
	v, err := c.Retrieve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if v.AccessKeyID != "AKW" {
		t.Errorf("got %+v", v)
	}
}

func TestDefaultPathsFromHome(t *testing.T) {
	home := t.TempDir()
	must(os.MkdirAll(filepath.Join(home, ".aws"), 0o755))
	must(os.WriteFile(filepath.Join(home, ".aws", "credentials"),
		[]byte("[default]\naws_access_key_id=DA\naws_secret_access_key=DS\n"), 0o600))
	// Default ConfigFile/CredentialsFile (empty) -> use HOME.
	c := DefaultChain(Config{Env: envMap(map[string]string{"HOME": home})})
	v, err := c.Retrieve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if v.AccessKeyID != "DA" {
		t.Errorf("got %+v", v)
	}
}

func TestProfileFromConfigFile(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config")
	must(os.WriteFile(cfgPath, []byte("[profile only-cfg]\naws_access_key_id=A\naws_secret_access_key=B\n"), 0o600))
	c := DefaultChain(Config{
		Profile:         "only-cfg",
		CredentialsFile: filepath.Join(dir, "no"),
		ConfigFile:      cfgPath,
		Env:             emptyEnv,
	})
	v, err := c.Retrieve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if v.AccessKeyID != "A" {
		t.Errorf("got %+v", v)
	}
}

func TestAccessKeyWithoutSecret(t *testing.T) {
	dir := t.TempDir()
	credPath := filepath.Join(dir, "credentials")
	must(os.WriteFile(credPath, []byte("[work]\naws_access_key_id=AK\n"), 0o600))
	c := DefaultChain(Config{Profile: "work", CredentialsFile: credPath, ConfigFile: filepath.Join(dir, "x"), Env: emptyEnv})
	if _, err := c.Retrieve(context.Background()); err == nil {
		t.Error("expected error")
	}
}

func TestProfileNotFound(t *testing.T) {
	c := DefaultChain(Config{
		Profile:         "missing",
		CredentialsFile: filepath.Join(t.TempDir(), "x"),
		ConfigFile:      filepath.Join(t.TempDir(), "y"),
		Env:             emptyEnv,
	})
	if _, err := c.Retrieve(context.Background()); err == nil {
		t.Error("expected error")
	}
}

func TestCredentialsFileLoadError(t *testing.T) {
	c := DefaultChain(Config{
		Profile:         "x",
		CredentialsFile: t.TempDir(), // directory -> read error
		ConfigFile:      filepath.Join(t.TempDir(), "no"),
		Env:             emptyEnv,
	})
	if _, err := c.Retrieve(context.Background()); err == nil {
		t.Error("expected error")
	}
}

func TestConfigFileLoadError(t *testing.T) {
	dir := t.TempDir()
	c := DefaultChain(Config{
		Profile:         "x",
		CredentialsFile: filepath.Join(dir, "no"),
		ConfigFile:      dir, // dir
		Env:             emptyEnv,
	})
	if _, err := c.Retrieve(context.Background()); err == nil {
		t.Error("expected error")
	}
}

func TestCredentialProcess(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config")
	must(os.WriteFile(cfgPath, []byte(`[profile p]
credential_process = /usr/bin/echo hello
`), 0o600))
	called := false
	exec := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		called = true
		if name != "/usr/bin/echo" || len(args) != 1 || args[0] != "hello" {
			t.Errorf("cmd: %s %v", name, args)
		}
		return []byte(`{"Version":1,"AccessKeyId":"AK","SecretAccessKey":"SK","SessionToken":"ST","Expiration":"2099-01-01T00:00:00Z"}`), nil
	}
	c := DefaultChain(Config{
		Profile:         "p",
		ConfigFile:      cfgPath,
		CredentialsFile: filepath.Join(dir, "no"),
		Env:             emptyEnv,
		Exec:            exec,
	})
	v, err := c.Retrieve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Error("exec not called")
	}
	if v.AccessKeyID != "AK" || v.SessionToken != "ST" || v.Expires.IsZero() {
		t.Errorf("got %+v", v)
	}
}

func TestCredentialProcessFromCredentialsFile(t *testing.T) {
	dir := t.TempDir()
	credPath := filepath.Join(dir, "credentials")
	must(os.WriteFile(credPath, []byte("[p]\ncredential_process = mycmd\n"), 0o600))
	exec := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return []byte(`{"Version":1,"AccessKeyId":"AK","SecretAccessKey":"SK"}`), nil
	}
	c := DefaultChain(Config{Profile: "p", CredentialsFile: credPath, ConfigFile: filepath.Join(dir, "no"), Env: emptyEnv, Exec: exec})
	if _, err := c.Retrieve(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestCredentialProcessErrors(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config")
	cases := []struct {
		name    string
		cmdline string
		exec    func(ctx context.Context, name string, args ...string) ([]byte, error)
	}{
		{"exec-fails", "x", func(context.Context, string, ...string) ([]byte, error) { return nil, errors.New("nope") }},
		{"bad-json", "x", func(context.Context, string, ...string) ([]byte, error) { return []byte("not json"), nil }},
		{"wrong-version", "x", func(context.Context, string, ...string) ([]byte, error) {
			return []byte(`{"Version":2,"AccessKeyId":"a","SecretAccessKey":"b"}`), nil
		}},
		{"missing-fields", "x", func(context.Context, string, ...string) ([]byte, error) {
			return []byte(`{"Version":1}`), nil
		}},
		{"bad-expiration", "x", func(context.Context, string, ...string) ([]byte, error) {
			return []byte(`{"Version":1,"AccessKeyId":"a","SecretAccessKey":"b","Expiration":"not-a-time"}`), nil
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			must(os.WriteFile(cfgPath, []byte("[profile p]\ncredential_process = "+c.cmdline+"\n"), 0o600))
			ch := DefaultChain(Config{
				Profile:         "p",
				ConfigFile:      cfgPath,
				CredentialsFile: filepath.Join(dir, "no"),
				Env:             emptyEnv,
				Exec:            c.exec,
			})
			if _, err := ch.Retrieve(context.Background()); err == nil {
				t.Error("expected error")
			}
		})
	}
}

func TestExpiryRefresh(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config")
	must(os.WriteFile(cfgPath, []byte("[profile p]\ncredential_process = x\n"), 0o600))

	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	calls := 0
	exec := func(context.Context, string, ...string) ([]byte, error) {
		calls++
		// Each call returns creds expiring 30s after "now" — well within the 1-min slack.
		return []byte(`{"Version":1,"AccessKeyId":"AK","SecretAccessKey":"SK","Expiration":"` + now.Add(30*time.Second).Format(time.RFC3339) + `"}`), nil
	}
	c := DefaultChain(Config{
		Profile:         "p",
		ConfigFile:      cfgPath,
		CredentialsFile: filepath.Join(dir, "no"),
		Env:             emptyEnv,
		Now:             func() time.Time { return now },
		Exec:            exec,
	})
	if _, err := c.Retrieve(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Retrieve(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Errorf("expected refresh on each call due to slack, calls=%d", calls)
	}
}

func TestCacheReusedBeforeExpiry(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config")
	must(os.WriteFile(cfgPath, []byte("[profile p]\ncredential_process = x\n"), 0o600))

	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	calls := 0
	exec := func(context.Context, string, ...string) ([]byte, error) {
		calls++
		return []byte(`{"Version":1,"AccessKeyId":"AK","SecretAccessKey":"SK","Expiration":"` + now.Add(time.Hour).Format(time.RFC3339) + `"}`), nil
	}
	c := DefaultChain(Config{
		Profile: "p", ConfigFile: cfgPath, CredentialsFile: filepath.Join(dir, "no"),
		Env: emptyEnv, Now: func() time.Time { return now }, Exec: exec,
	})
	for i := 0; i < 3; i++ {
		if _, err := c.Retrieve(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Errorf("expected cache reuse, calls=%d", calls)
	}
}

func TestSplitCmdline(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"a", []string{"a"}},
		{"  a  b  c ", []string{"a", "b", "c"}},
		{`a "b c" d`, []string{"a", "b c", "d"}},
		{"a\tb", []string{"a", "b"}},
	}
	for _, c := range cases {
		got := splitCmdline(c.in)
		if len(got) != len(c.want) {
			t.Errorf("%q: got %v want %v", c.in, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%q: got %v want %v", c.in, got, c.want)
				break
			}
		}
	}
}

func TestHomeDirEnv(t *testing.T) {
	if got := homeDir(envMap(map[string]string{"HOME": "/x"})); got != "/x" {
		t.Errorf("got %q", got)
	}
	// Empty HOME -> falls back to os.UserHomeDir; we just assert non-panic.
	_ = homeDir(emptyEnv)
}

func TestHomeDirFallback(t *testing.T) {
	// Force os.UserHomeDir to fail by clearing HOME at the OS level too.
	t.Setenv("HOME", "")
	t.Setenv("USERPROFILE", "")
	_ = homeDir(emptyEnv) // result depends on platform; just must not panic
}

func TestDefaultExecRunsRealCommand(t *testing.T) {
	// Exercise the default Exec path (no override) by pointing
	// credential_process at a small shell script that emits valid JSON.
	dir := t.TempDir()
	script := filepath.Join(dir, "cp.sh")
	must(os.WriteFile(script, []byte("#!/bin/sh\necho '{\"Version\":1,\"AccessKeyId\":\"AK\",\"SecretAccessKey\":\"SK\"}'\n"), 0o755))
	cfgPath := filepath.Join(dir, "config")
	must(os.WriteFile(cfgPath, []byte("[profile p]\ncredential_process = "+script+"\n"), 0o600))
	c := DefaultChain(Config{
		Profile: "p", ConfigFile: cfgPath, CredentialsFile: filepath.Join(dir, "no"),
		Env: emptyEnv,
	})
	v, err := c.Retrieve(context.Background())
	if err != nil {
		t.Fatalf("default exec failed: %v", err)
	}
	if v.AccessKeyID != "AK" {
		t.Errorf("got %+v", v)
	}
}

func TestDefaultNowAndEnv(t *testing.T) {
	// Cover Config.now() and Config.env() defaults by leaving them nil.
	cfg := Config{}
	if cfg.now().IsZero() {
		t.Error("now default")
	}
	t.Setenv("BEDROCK_LIGHT_TEST_KEY", "v")
	if cfg.env("BEDROCK_LIGHT_TEST_KEY") != "v" {
		t.Error("env default")
	}
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}
