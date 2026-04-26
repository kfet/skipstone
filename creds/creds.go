// Package creds resolves AWS credentials for SigV4 signing.
//
// Resolution order:
//  1. Static creds passed via WithStatic (highest priority).
//  2. Environment: AWS_ACCESS_KEY_ID + AWS_SECRET_ACCESS_KEY (+ optional AWS_SESSION_TOKEN).
//  3. Shared credentials/config files for the active profile:
//       - aws_access_key_id / aws_secret_access_key / aws_session_token
//       - credential_process (executes a command, parses JSON from stdout)
//
// Out of scope (use the AWS CLI / granted `assume` upstream):
//   - SSO login flow + token cache
//   - STS AssumeRole / source_profile chains
//   - MFA, IMDS, ECS task creds, web identity / IRSA
package creds

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/kfet/bedrock-light/awsini"
)

// Value is a set of resolved credentials. Expires is zero for non-temporary creds.
type Value struct {
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string
	Expires         time.Time
}

// Provider returns credentials, refreshing as needed.
type Provider interface {
	Retrieve(ctx context.Context) (Value, error)
}

// ProviderFunc adapts an ordinary function to Provider.
type ProviderFunc func(ctx context.Context) (Value, error)

// Retrieve implements Provider.
func (f ProviderFunc) Retrieve(ctx context.Context) (Value, error) { return f(ctx) }

// Static returns a Provider that always yields the given credentials.
func Static(ak, sk, st string) Provider {
	v := Value{AccessKeyID: ak, SecretAccessKey: sk, SessionToken: st}
	return ProviderFunc(func(context.Context) (Value, error) { return v, nil })
}

// Config controls credential resolution.
type Config struct {
	// Profile is the AWS profile name. Defaults to AWS_PROFILE or "default".
	Profile string
	// ConfigFile and CredentialsFile override the default ~/.aws paths.
	ConfigFile      string
	CredentialsFile string
	// Env is the environment lookup function. Defaults to os.Getenv.
	Env func(string) string
	// Now is the current time. Defaults to time.Now.
	Now func() time.Time
	// Exec runs a credential_process command. Defaults to exec.CommandContext.
	Exec func(ctx context.Context, name string, args ...string) ([]byte, error)
}

func (c *Config) env(k string) string {
	if c.Env != nil {
		return c.Env(k)
	}
	return os.Getenv(k)
}

func (c *Config) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Config) execCmd(ctx context.Context, name string, args ...string) ([]byte, error) {
	if c.Exec != nil {
		return c.Exec(ctx, name, args...)
	}
	return exec.CommandContext(ctx, name, args...).Output()
}

// DefaultChain returns a Provider implementing the documented resolution order.
func DefaultChain(cfg Config) Provider {
	chain := &chain{cfg: cfg}
	return chain
}

type chain struct {
	cfg Config

	mu     sync.Mutex
	cached Value
	have   bool
}

// Retrieve resolves credentials, refreshing temporary ones on expiry.
func (c *chain) Retrieve(ctx context.Context) (Value, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.have && !c.expired() {
		return c.cached, nil
	}
	v, err := c.resolve(ctx)
	if err != nil {
		return Value{}, err
	}
	c.cached = v
	c.have = true
	return v, nil
}

func (c *chain) expired() bool {
	if c.cached.Expires.IsZero() {
		return false
	}
	// 1-minute slack to avoid using creds that expire mid-flight.
	return !c.cfg.now().Add(time.Minute).Before(c.cached.Expires)
}

func (c *chain) resolve(ctx context.Context) (Value, error) {
	// 1. Environment.
	if ak := c.cfg.env("AWS_ACCESS_KEY_ID"); ak != "" {
		sk := c.cfg.env("AWS_SECRET_ACCESS_KEY")
		if sk == "" {
			return Value{}, errors.New("creds: AWS_ACCESS_KEY_ID set without AWS_SECRET_ACCESS_KEY")
		}
		return Value{
			AccessKeyID:     ak,
			SecretAccessKey: sk,
			SessionToken:    c.cfg.env("AWS_SESSION_TOKEN"),
		}, nil
	}

	// 2. Shared files.
	profile := c.cfg.Profile
	if profile == "" {
		profile = c.cfg.env("AWS_PROFILE")
	}
	if profile == "" {
		profile = "default"
	}

	credPath := c.cfg.CredentialsFile
	if credPath == "" {
		credPath = filepath.Join(homeDir(c.cfg.env), ".aws", "credentials")
	}
	cfgPath := c.cfg.ConfigFile
	if cfgPath == "" {
		cfgPath = filepath.Join(homeDir(c.cfg.env), ".aws", "config")
	}

	credFile, err := awsini.LoadCredentials(credPath)
	if err != nil {
		return Value{}, fmt.Errorf("creds: load credentials: %w", err)
	}
	confFile, err := awsini.LoadConfig(cfgPath)
	if err != nil {
		return Value{}, fmt.Errorf("creds: load config: %w", err)
	}

	// Static keys in either credentials or config.
	for _, src := range []awsini.File{credFile, confFile} {
		ak := src.Get(profile, "aws_access_key_id")
		if ak == "" {
			continue
		}
		sk := src.Get(profile, "aws_secret_access_key")
		if sk == "" {
			return Value{}, fmt.Errorf("creds: profile %q has access key without secret", profile)
		}
		return Value{
			AccessKeyID:     ak,
			SecretAccessKey: sk,
			SessionToken:    src.Get(profile, "aws_session_token"),
		}, nil
	}

	// credential_process — config first, then credentials.
	for _, src := range []awsini.File{confFile, credFile} {
		if cmd := src.Get(profile, "credential_process"); cmd != "" {
			return c.runCredentialProcess(ctx, cmd)
		}
	}

	return Value{}, fmt.Errorf("creds: no credentials found for profile %q", profile)
}

// processOutput is the JSON shape emitted by credential_process commands.
// Fields named per https://docs.aws.amazon.com/sdkref/latest/guide/feature-process-credentials.html
type processOutput struct {
	Version         int    `json:"Version"`
	AccessKeyID     string `json:"AccessKeyId"`
	SecretAccessKey string `json:"SecretAccessKey"`
	SessionToken    string `json:"SessionToken"`
	Expiration      string `json:"Expiration"`
}

func (c *chain) runCredentialProcess(ctx context.Context, cmdline string) (Value, error) {
	args := splitCmdline(cmdline)
	out, err := c.cfg.execCmd(ctx, args[0], args[1:]...)
	if err != nil {
		return Value{}, fmt.Errorf("creds: credential_process: %w", err)
	}
	var p processOutput
	if err := json.Unmarshal(out, &p); err != nil {
		return Value{}, fmt.Errorf("creds: credential_process output: %w", err)
	}
	if p.Version != 1 {
		return Value{}, fmt.Errorf("creds: credential_process version=%d (want 1)", p.Version)
	}
	if p.AccessKeyID == "" || p.SecretAccessKey == "" {
		return Value{}, errors.New("creds: credential_process missing access key or secret")
	}
	v := Value{
		AccessKeyID:     p.AccessKeyID,
		SecretAccessKey: p.SecretAccessKey,
		SessionToken:    p.SessionToken,
	}
	if p.Expiration != "" {
		t, err := time.Parse(time.RFC3339, p.Expiration)
		if err != nil {
			return Value{}, fmt.Errorf("creds: credential_process Expiration: %w", err)
		}
		v.Expires = t
	}
	return v, nil
}

// splitCmdline does a minimal POSIX-shell-ish split: whitespace separates
// tokens; double-quoted spans preserve spaces. Backslash-escapes are not
// supported (rare in credential_process; users can wrap in /bin/sh -c).
func splitCmdline(s string) []string {
	var out []string
	var cur strings.Builder
	inQuote := false
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if ch == '"' {
			inQuote = !inQuote
			continue
		}
		if !inQuote && (ch == ' ' || ch == '\t') {
			if cur.Len() > 0 {
				out = append(out, cur.String())
				cur.Reset()
			}
			continue
		}
		cur.WriteByte(ch)
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

func homeDir(env func(string) string) string {
	if h := env("HOME"); h != "" {
		return h
	}
	if h, err := os.UserHomeDir(); err == nil {
		return h
	}
	return ""
}
