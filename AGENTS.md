# AGENTS.md — bedrock-light

## What this project is

A **minimal, zero-dependency Go client for Amazon Bedrock's `ConverseStream`
API**. Intended to replace `github.com/aws/aws-sdk-go-v2/service/bedrockruntime`
in projects that:

- Use exactly one Bedrock API (`ConverseStream`).
- Acquire AWS credentials externally (env vars, long-term IAM keys in `~/.aws/credentials`).
- Care about binary size, build time, and dependency surface.

The original motivating consumer is [fir](https://github.com/kfet/fir), where
the AWS SDK pulled in **16 transitive `aws/*` modules + smithy-go (~58MB)** to
support one streaming RPC.

## Motivation

The full AWS SDK is built to support every AWS service, every credential
source, and every endpoint variation. For a project that calls one Bedrock API
with credentials already on disk, that's massive overkill:

| Concern | AWS SDK | bedrock-light |
|---|---|---|
| Runtime modules | 16 | 0 |
| Module cache | ~58 MB | 0 |
| Credential sources | env, profile, SSO+OIDC refresh, STS chains, IMDS, ECS, IRSA, MFA, web identity, credential_process | env, profile, credential_process, STS AssumeRole chains (incl. MFA), IRSA, ECS, IMDSv2 |
| APIs | all of Bedrock + STS + SSO + … | `ConverseStream` (+ STS for AssumeRole / web identity) |
| Type ergonomics | `brtypes.ContentBlockMemberText{Value: brtypes.TextBlock{...}}` | `bedrocklight.Block{Text: "..."}` |

We **deliberately don't** support the SSO login flow (OIDC device-code +
token cache refresh) — that's the one painful, security-sensitive flow we
delegate entirely to the AWS CLI / `assume`. STS AssumeRole, MFA prompts,
IRSA, ECS, and IMDSv2 are all small enough to implement directly against
their wire protocols, so we do; see "What's in scope" below.

## What's in scope

Anything required to call `ConverseStream` with credentials acquired through:

- **Environment variables**: `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`,
  `AWS_SESSION_TOKEN`.
- **`~/.aws/credentials`** static profiles (long-term keys or `assume`-written
  short-term keys).
- **`credential_process`** (executes a command, parses JSON from stdout).
- **STS `AssumeRole` chains** via `role_arn` + `source_profile` /
  `credential_source`, with optional `mfa_serial` (interactive token prompt),
  `external_id`, `duration_seconds`, `role_session_name`.
- **Web identity / IRSA**: `AWS_WEB_IDENTITY_TOKEN_FILE` + `AWS_ROLE_ARN`
  (AssumeRoleWithWebIdentity, regional STS endpoint).
- **ECS task creds**: `AWS_CONTAINER_CREDENTIALS_RELATIVE_URI` /
  `_FULL_URI` (with optional `AWS_CONTAINER_AUTHORIZATION_TOKEN[_FILE]`).
- **EC2 IMDSv2** instance role (honors `AWS_EC2_METADATA_DISABLED`).
- **Region** from env, profile config, or default.
- **Endpoint** override via env or option (FIPS, LocalStack, custom VPC).

Wire concerns:

- SigV4 signing of bounded JSON / form-urlencoded request bodies.
- Decoding `application/vnd.amazon.eventstream` framing in responses.
- Retry with exponential backoff on 429/5xx, honoring `Retry-After`.

## What's out of scope (do NOT add without discussion)

- SSO login flow / token cache / OIDC refresh — use `aws sso login` upstream.
- Other Bedrock APIs (`InvokeModel`, non-stream `Converse`).
- Presigned URLs, query-string auth.
- Streaming-payload signing (`STREAMING-AWS4-HMAC-SHA256-PAYLOAD`).
- Adaptive retry, request compression, chunked transfer encoding.

If a real user need surfaces for any of the above, **open an issue first** so
we can decide whether the cost (LOC, complexity, security surface) is worth
the value before any code is written.

## Architecture

The library is laid out as a small set of focused packages. The structure
below describes responsibilities, not individual files — refer to the source
or `go doc` for the file-level breakdown.

```
bedrocklight/      root package — public API
                   (Client, ConverseStream, Stream, Event*, options, types)

├── creds/         credential resolver chain
│                  env • shared profile • credential_process
│                  STS AssumeRole (incl. MFA, source_profile, credential_source)
│                  IRSA (AssumeRoleWithWebIdentity) • ECS task creds • EC2 IMDSv2

├── sigv4/         AWS Signature Version 4 signer (bounded JSON / form bodies)

├── eventstream/   AWS event-stream frame codec
│                  (prelude + headers + payload + CRC32)

├── internal/
│   └── awsini/    AWS shared-ini parser for ~/.aws/{config,credentials}

└── e2e/           separate go.mod — AWS SDK pulled only here
                   cross-checks SigV4 + event-stream against aws-sdk-go-v2
```

The root package owns the public API; everything under it is an
implementation detail that may move or split without a major version bump.
`internal/` is enforced by the Go toolchain; `creds/`, `sigv4/`, and
`eventstream/` are exported only because their types appear in option
signatures or are useful in isolation, not because they're a stable
sub-API surface.

### Why `e2e/` is a separate Go module

The whole point of bedrock-light is to **not** depend on the AWS SDK. But we
still want to verify behavioural equivalence for the high-risk parts
(SigV4 + event-stream framing). Keeping the cross-checks in a separate module
with its own `go.mod` means:

- `go test ./...` from the repo root touches **only** stdlib.
- `make e2e` (or `cd e2e && go test`) pulls AWS SDK at that moment for
  cross-validation, but those modules never enter the main `go.mod`.
- Consumers vendoring or auditing the library see zero AWS SDK in their
  dependency graph.

## Public API surface

Stable contract — additions are fine, removals/renames require a major bump.

```go
type Client struct{ /* unexported */ }

func NewClient(opts ...Option) (*Client, error)

// Options
func WithRegion(r string) Option
func WithEndpoint(u string) Option
func WithHTTPClient(c *http.Client) Option
func WithCredentials(p creds.Provider) Option
func WithStaticCredentials(ak, sk, st string) Option
func WithProfile(name string) Option
func WithNow(fn func() time.Time) Option
func WithRetries(n int) Option
func WithBackoff(fn func(attempt int) time.Duration) Option

// RPC
func (c *Client) ConverseStream(ctx context.Context, in *ConverseStreamInput) (*Stream, error)

// Streaming
type Stream struct{ /* ... */ }
func (s *Stream) Recv() (Event, error)   // io.EOF at end
func (s *Stream) Close() error

// Event payload types: EventMessageStart, EventContentBlockStart,
// EventContentBlockDelta, EventContentBlockStop, EventMessageStop,
// EventMetadata. Unknown event types surface with Event.Decoded == nil
// and the raw JSON in Event.Raw.
```

## House rules

- **100% test coverage**, enforced at build time (`make test`). Use
  `.covignore` only for files/dirs that are deliberately out of the unit
  coverage profile (e.g. the `e2e/` module). If a line is genuinely
  uncoverable, delete it instead of ignoring it.
- **Zero runtime dependencies.** `go.mod` lists no `require` block; `go test
  ./...` works offline. The `e2e/` module is the only exception. If you want
  to pull in a dep, write 30 LOC instead.
- **Idiomatic Go, simple code.** Small focused packages, stdlib-only tests,
  errors prefixed with `"bedrocklight: "` and wrapped with `%w`.

### Adding new features

1. Justify it — does it belong in this library, or in caller code?
2. Update this `AGENTS.md` if scope changes.
3. Failing test first.
4. Implement.
5. Cover to 100%. Don't add `.covignore` entries.
6. Cross-check in `e2e/` if the change touches SigV4 or event-stream framing.

## Roadmap

These are explicitly **deferred** until a real user need surfaces:

- Cross-check `ConverseStream` end-to-end against the AWS SDK in `e2e/`
  (build the same input, capture both wire payloads, diff). Today we only
  cross-check SigV4 + event-stream framing.
- A few more `Event*` typed payloads as Bedrock adds new content block kinds.
- Optional `httptrace` hooks for debugging.
- Pluggable retry classifier (right now retries are hardcoded on 429/5xx).

## Out of scope

- Other AWS services. If you need S3, use the SDK.
- Synchronous (non-streaming) `Converse`.
- Any feature that requires an outbound network call to a non-Bedrock
  endpoint (STS, SSO portal, OIDC). Those belong in the credential-acquisition
  step, which we delegate to upstream tools by design.

## Build

Run `make`.
