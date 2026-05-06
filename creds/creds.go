// Package creds resolves AWS credentials for SigV4 signing.
//
// Resolution order in DefaultChain:
//  1. Static creds passed via WithStatic (highest priority, short-circuits chain).
//  2. Environment: AWS_ACCESS_KEY_ID + AWS_SECRET_ACCESS_KEY (+ optional AWS_SESSION_TOKEN).
//  3. AssumeRoleWithWebIdentity (IRSA): AWS_WEB_IDENTITY_TOKEN_FILE + AWS_ROLE_ARN.
//  4. ECS task creds: AWS_CONTAINER_CREDENTIALS_RELATIVE_URI / _FULL_URI.
//  5. Shared credentials/config files for the active profile, supporting:
//     - aws_access_key_id / aws_secret_access_key / aws_session_token
//     - credential_process (executes a command, parses JSON from stdout)
//     - role_arn + source_profile / credential_source (STS AssumeRole, MFA)
//  6. EC2 IMDSv2 (unless AWS_EC2_METADATA_DISABLED=true).
//
// Out of scope (use the AWS CLI / granted `assume` upstream):
//   - SSO login flow + token cache / OIDC refresh
package creds

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/kfet/bedrock-light/internal/awsini"
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
	// Region overrides region resolution for STS calls (AssumeRole / IRSA).
	// If empty, uses profile's region= then AWS_REGION/AWS_DEFAULT_REGION then us-east-1.
	Region string
	// ConfigFile and CredentialsFile override the default ~/.aws paths.
	ConfigFile      string
	CredentialsFile string
	// Env is the environment lookup function. Defaults to os.Getenv.
	Env func(string) string
	// Now is the current time. Defaults to time.Now.
	Now func() time.Time
	// Exec runs a credential_process command. Defaults to exec.CommandContext.
	Exec func(ctx context.Context, name string, args ...string) ([]byte, error)
	// HTTPClient is used for IMDS / ECS / STS / IRSA calls. Defaults to http.DefaultClient.
	HTTPClient *http.Client
	// IMDSEndpoint overrides the IMDSv2 base URL (default http://169.254.169.254).
	IMDSEndpoint string
	// ECSEndpoint overrides the ECS metadata host (default http://169.254.170.2)
	// for AWS_CONTAINER_CREDENTIALS_RELATIVE_URI.
	ECSEndpoint string
	// STSEndpoint overrides the STS endpoint (default https://sts.<region>.amazonaws.com).
	STSEndpoint string
	// MFATokenProvider returns an MFA token code for the given serial. If nil,
	// the default reads a line from Stdin after writing a prompt to Stderr.
	MFATokenProvider func(serial string) (string, error)
	// Stdin / Stderr default to os.Stdin / os.Stderr (used by the default MFA prompt).
	Stdin  io.Reader
	Stderr io.Writer
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

func (c *Config) httpClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return http.DefaultClient
}

// errNoMatch signals a provider had nothing to contribute; the chain should try the next one.
var errNoMatch = errors.New("creds: no match")

// DefaultChain returns a Provider implementing the documented resolution order.
func DefaultChain(cfg Config) Provider {
	return &chain{cfg: cfg}
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
	providers := []func() (Value, error){
		func() (Value, error) { return envProvider(&c.cfg) },
		func() (Value, error) { return webIdentityProvider(ctx, &c.cfg) },
		func() (Value, error) { return ecsProvider(ctx, &c.cfg) },
		func() (Value, error) { return sharedProfileProvider(ctx, &c.cfg) },
		func() (Value, error) { return imdsProvider(ctx, &c.cfg) },
	}
	for _, p := range providers {
		v, err := p()
		if err == nil {
			return v, nil
		}
		if !errors.Is(err, errNoMatch) {
			return Value{}, err
		}
	}
	return Value{}, errors.New("creds: no credentials found in environment, IRSA, ECS, shared profile, or IMDS")
}

func envProvider(cfg *Config) (Value, error) {
	ak := cfg.env("AWS_ACCESS_KEY_ID")
	if ak == "" {
		return Value{}, errNoMatch
	}
	sk := cfg.env("AWS_SECRET_ACCESS_KEY")
	if sk == "" {
		return Value{}, errors.New("creds: AWS_ACCESS_KEY_ID set without AWS_SECRET_ACCESS_KEY")
	}
	return Value{
		AccessKeyID:     ak,
		SecretAccessKey: sk,
		SessionToken:    cfg.env("AWS_SESSION_TOKEN"),
	}, nil
}

// resolveProfileName returns the active profile name.
func resolveProfileName(cfg *Config) string {
	p := cfg.Profile
	if p == "" {
		p = cfg.env("AWS_PROFILE")
	}
	if p == "" {
		p = "default"
	}
	return p
}

func loadIniFiles(cfg *Config) (cred, conf awsini.File, err error) {
	credPath := cfg.CredentialsFile
	if credPath == "" {
		credPath = filepath.Join(homeDir(cfg.env), ".aws", "credentials")
	}
	cfgPath := cfg.ConfigFile
	if cfgPath == "" {
		cfgPath = filepath.Join(homeDir(cfg.env), ".aws", "config")
	}
	cred, err = awsini.LoadCredentials(credPath)
	if err != nil {
		return nil, nil, fmt.Errorf("creds: load credentials: %w", err)
	}
	conf, err = awsini.LoadConfig(cfgPath)
	if err != nil {
		return nil, nil, fmt.Errorf("creds: load config: %w", err)
	}
	return cred, conf, nil
}

// sharedProfileProvider resolves creds from the shared ini files for the active profile,
// including AssumeRole / source_profile / credential_source chains. Returns errNoMatch
// if the profile section is absent in both files (so the chain can fall through to IMDS).
func sharedProfileProvider(ctx context.Context, cfg *Config) (Value, error) {
	profile := resolveProfileName(cfg)
	cred, conf, err := loadIniFiles(cfg)
	if err != nil {
		return Value{}, err
	}
	if _, ok := cred[profile]; !ok {
		if _, ok2 := conf[profile]; !ok2 {
			return Value{}, errNoMatch
		}
	}
	return resolveProfile(ctx, cfg, cred, conf, profile, map[string]bool{})
}

// resolveProfile resolves a named profile, recursing through source_profile chains.
func resolveProfile(ctx context.Context, cfg *Config, cred, conf awsini.File, profile string, visited map[string]bool) (Value, error) {
	if visited[profile] {
		return Value{}, fmt.Errorf("creds: source_profile cycle through %q", profile)
	}
	visited[profile] = true

	// role_arn => AssumeRole chain.
	roleArn := profileGet(cred, conf, profile, "role_arn")
	if roleArn != "" {
		return assumeRoleFromProfile(ctx, cfg, cred, conf, profile, roleArn, visited)
	}

	// Static keys (credentials wins over config per AWS rules).
	for _, src := range []awsini.File{cred, conf} {
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
	for _, src := range []awsini.File{conf, cred} {
		if cmd := src.Get(profile, "credential_process"); cmd != "" {
			return runCredentialProcess(ctx, cfg, cmd)
		}
	}

	return Value{}, fmt.Errorf("creds: no credentials found for profile %q", profile)
}

// profileGet looks up a key in either credentials or config (credentials wins).
func profileGet(cred, conf awsini.File, profile, key string) string {
	if v := cred.Get(profile, key); v != "" {
		return v
	}
	return conf.Get(profile, key)
}

// processOutput is the JSON shape emitted by credential_process commands.
type processOutput struct {
	Version         int    `json:"Version"`
	AccessKeyID     string `json:"AccessKeyId"`
	SecretAccessKey string `json:"SecretAccessKey"`
	SessionToken    string `json:"SessionToken"`
	Expiration      string `json:"Expiration"`
}

func runCredentialProcess(ctx context.Context, cfg *Config, cmdline string) (Value, error) {
	args := splitCmdline(cmdline)
	out, err := cfg.execCmd(ctx, args[0], args[1:]...)
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

// resolveRegion picks the STS/region for AssumeRole + IRSA flows.
func resolveRegion(cfg *Config, profileRegion string) string {
	if cfg.Region != "" {
		return cfg.Region
	}
	if profileRegion != "" {
		return profileRegion
	}
	if r := cfg.env("AWS_REGION"); r != "" {
		return r
	}
	if r := cfg.env("AWS_DEFAULT_REGION"); r != "" {
		return r
	}
	return "us-east-1"
}
