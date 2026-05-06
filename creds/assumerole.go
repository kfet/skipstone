package creds

import (
	"bufio"
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/kfet/bedrock-light/internal/awsini"
	"github.com/kfet/bedrock-light/sigv4"
)

// assumeRoleFromProfile resolves source creds (via source_profile or
// credential_source) and calls STS AssumeRole signed with them.
func assumeRoleFromProfile(ctx context.Context, cfg *Config, cred, conf awsini.File, profile, roleArn string, visited map[string]bool) (Value, error) {
	sourceProfile := profileGet(cred, conf, profile, "source_profile")
	credentialSource := profileGet(cred, conf, profile, "credential_source")
	if sourceProfile == "" && credentialSource == "" {
		return Value{}, fmt.Errorf("creds: profile %q has role_arn but no source_profile or credential_source", profile)
	}
	if sourceProfile != "" && credentialSource != "" {
		return Value{}, fmt.Errorf("creds: profile %q has both source_profile and credential_source", profile)
	}

	var srcCreds Value
	var err error
	if sourceProfile != "" {
		srcCreds, err = resolveProfile(ctx, cfg, cred, conf, sourceProfile, visited)
	} else {
		srcCreds, err = resolveCredentialSource(ctx, cfg, credentialSource)
	}
	if err != nil {
		return Value{}, fmt.Errorf("creds: source for profile %q: %w", profile, err)
	}

	region := resolveRegion(cfg, profileGet(cred, conf, profile, "region"))
	sessionName := profileGet(cred, conf, profile, "role_session_name")
	if sessionName == "" {
		sessionName = "bedrock-light-" + strconv.FormatInt(cfg.now().UnixNano(), 10)
	}
	durationStr := profileGet(cred, conf, profile, "duration_seconds")
	externalID := profileGet(cred, conf, profile, "external_id")
	mfaSerial := profileGet(cred, conf, profile, "mfa_serial")

	var tokenCode string
	if mfaSerial != "" {
		tokenCode, err = mfaToken(cfg, mfaSerial)
		if err != nil {
			return Value{}, err
		}
	}

	return callAssumeRole(ctx, cfg, region, srcCreds, roleArn, sessionName, durationStr, externalID, mfaSerial, tokenCode)
}

func resolveCredentialSource(ctx context.Context, cfg *Config, source string) (Value, error) {
	switch source {
	case "Environment":
		v, err := envProvider(cfg)
		if errors.Is(err, errNoMatch) {
			return Value{}, errors.New("creds: credential_source=Environment but no env creds set")
		}
		return v, err
	case "Ec2InstanceMetadata":
		v, err := imdsProvider(ctx, cfg)
		if errors.Is(err, errNoMatch) {
			return Value{}, errors.New("creds: credential_source=Ec2InstanceMetadata unavailable")
		}
		return v, err
	case "EcsContainer":
		v, err := ecsProvider(ctx, cfg)
		if errors.Is(err, errNoMatch) {
			return Value{}, errors.New("creds: credential_source=EcsContainer but no ECS creds env set")
		}
		return v, err
	default:
		return Value{}, fmt.Errorf("creds: unknown credential_source %q", source)
	}
}

func mfaToken(cfg *Config, serial string) (string, error) {
	if cfg.MFATokenProvider != nil {
		return cfg.MFATokenProvider(serial)
	}
	stdin := cfg.Stdin
	if stdin == nil {
		stdin = os.Stdin
	}
	stderr := cfg.Stderr
	if stderr == nil {
		stderr = os.Stderr
	}
	fmt.Fprintf(stderr, "Enter MFA code for %s: ", serial)
	br := bufio.NewReader(stdin)
	line, err := br.ReadString('\n')
	if err != nil && err != io.EOF {
		return "", fmt.Errorf("creds: read MFA token: %w", err)
	}
	code := strings.TrimSpace(line)
	if code == "" {
		return "", errors.New("creds: empty MFA token code")
	}
	return code, nil
}

// callAssumeRole performs a SigV4-signed STS AssumeRole call and decodes the response.
func callAssumeRole(ctx context.Context, cfg *Config, region string, srcCreds Value, roleArn, sessionName, durationStr, externalID, mfaSerial, tokenCode string) (Value, error) {
	form := url.Values{}
	form.Set("Action", "AssumeRole")
	form.Set("Version", "2011-06-15")
	form.Set("RoleArn", roleArn)
	form.Set("RoleSessionName", sessionName)
	if durationStr != "" {
		// Validate it's an int for nicer errors.
		if _, err := strconv.Atoi(durationStr); err != nil {
			return Value{}, fmt.Errorf("creds: duration_seconds %q: %w", durationStr, err)
		}
		form.Set("DurationSeconds", durationStr)
	}
	if externalID != "" {
		form.Set("ExternalId", externalID)
	}
	if mfaSerial != "" {
		form.Set("SerialNumber", mfaSerial)
		form.Set("TokenCode", tokenCode)
	}
	body := form.Encode()

	endpoint := stsEndpoint(cfg, region)
	req, _ := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
	req.Header.Set("Accept", "application/xml")

	signer := &sigv4.Signer{Region: region, Service: "sts"}
	// Sign cannot fail here: region/service are non-empty, the endpoint is a
	// well-formed URL, and srcCreds are guaranteed non-empty by the resolvers
	// that produced them.
	_ = signer.Sign(req, sigv4.Credentials{
		AccessKeyID:     srcCreds.AccessKeyID,
		SecretAccessKey: srcCreds.SecretAccessKey,
		SessionToken:    srcCreds.SessionToken,
	}, cfg.now())

	resp, err := cfg.httpClient().Do(req)
	if err != nil {
		return Value{}, fmt.Errorf("creds: sts do: %w", err)
	}
	respBody := readAndClose(resp)
	if resp.StatusCode/100 != 2 {
		return Value{}, parseSTSError(respBody, resp.StatusCode)
	}
	var out struct {
		XMLName xml.Name `xml:"AssumeRoleResponse"`
		Result  struct {
			Credentials stsCredentialsXML `xml:"Credentials"`
		} `xml:"AssumeRoleResult"`
	}
	if err := xml.Unmarshal(respBody, &out); err != nil {
		return Value{}, fmt.Errorf("creds: sts xml: %w", err)
	}
	return out.Result.Credentials.toValue("sts")
}
