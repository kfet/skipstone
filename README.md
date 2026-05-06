# skipstone

<!-- TODO(badges): once the GitHub repo is published, add:
       - CI status:    ![CI](https://github.com/kfet/skipstone/actions/workflows/test.yml/badge.svg)
       - pkg.go.dev:   [![Go Reference](https://pkg.go.dev/badge/github.com/kfet/skipstone.svg)](https://pkg.go.dev/github.com/kfet/skipstone)
       - Go report:    [![Go Report Card](https://goreportcard.com/badge/github.com/kfet/skipstone)](https://goreportcard.com/report/github.com/kfet/skipstone)
-->

Minimal, zero-dependency Go client for **Amazon Bedrock `ConverseStream`**.

Stdlib only. No `aws-sdk-go-v2`, no `smithy-go`. Designed for tools that need exactly one Bedrock API and acquire credentials through external tooling such as [granted.dev `assume`](https://granted.dev/) or long-term IAM keys.

## What's supported

### Credentials
- Environment variables (`AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, `AWS_SESSION_TOKEN`)
- Static keys from `~/.aws/credentials` profiles
- `credential_process` (executes a command, parses JSON from stdout)
- Profile selection via `AWS_PROFILE`
- **STS `AssumeRole` chains** via `role_arn` + `source_profile` / `credential_source` (Environment / Ec2InstanceMetadata / EcsContainer), with optional `mfa_serial` (prompts on stdin), `external_id`, `duration_seconds`, `role_session_name`
- **Web identity / IRSA** via `AWS_WEB_IDENTITY_TOKEN_FILE` + `AWS_ROLE_ARN` (AssumeRoleWithWebIdentity)
- **ECS task creds** via `AWS_CONTAINER_CREDENTIALS_RELATIVE_URI` / `_FULL_URI` (with `AWS_CONTAINER_AUTHORIZATION_TOKEN` / `_TOKEN_FILE`)
- **EC2 IMDSv2** instance role (token-based, honors `AWS_EC2_METADATA_DISABLED`)

### Region / endpoint
- `AWS_REGION` / `AWS_DEFAULT_REGION` env
- `region = ...` from `~/.aws/config` (the active profile)
- Endpoint override via `AWS_ENDPOINT_URL_BEDROCK_RUNTIME` or `WithEndpoint`

### API
- **`ConverseStream`** only (the API fir uses)
- Text, image, tool_use, tool_result, reasoning, cache_point content blocks
- Tool config (incl. tool choice)
- Inference config (max tokens, temperature, top-p, stop sequences)
- Streaming events: messageStart, contentBlockStart/Delta/Stop, messageStop,
  metadata. Decoded delta covers text, tool_use input, reasoning text /
  signature / redacted content, and citation payloads. Decoded metadata
  covers token usage (incl. cache read/write), latency, guardrail trace,
  performance config, and service tier.

### Wire
- SigV4 request signing (no presigning, no streaming-payload signing)
- AWS event-stream response decoding (prelude + headers + payload + CRC32)
- Retry with exponential backoff on 429/5xx, honoring `Retry-After`
  (override the policy with `WithRetryClassifier`)

## What's _not_ supported

- SSO login flow / token cache (use `aws sso login` or `assume`)
- Other Bedrock APIs (`InvokeModel`, non-stream `Converse`)

## Dependencies

Runtime: **none** (Go stdlib only).
Tests: stdlib only.
e2e module (separate `go.mod`): pulls the AWS SDK at build time only, to cross-check SigV4 + event-stream behavior against the reference implementation.

## License

MIT
