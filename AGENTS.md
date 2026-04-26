# AGENTS.md — bedrock-light

Authoritative guide for AI agents (and humans) contributing to this repo.
Read this before making changes.

## What this project is

A **minimal, zero-dependency Go client for Amazon Bedrock's `ConverseStream`
API**. Intended to replace `github.com/aws/aws-sdk-go-v2/service/bedrockruntime`
in projects that:

- Use exactly one Bedrock API (`ConverseStream`).
- Acquire AWS credentials externally (env vars, long-term IAM keys in
  `~/.aws/credentials`, or tools like [granted.dev `assume`](https://granted.dev)
  that write resolved short-term creds back to env or the credentials file).
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
| Credential sources | env, profile, SSO+OIDC refresh, STS chains, IMDS, ECS, IRSA, MFA, web identity, credential_process | env, profile, credential_process |
| APIs | all of Bedrock + STS + SSO + … | `ConverseStream` |
| Type ergonomics | `brtypes.ContentBlockMemberText{Value: brtypes.TextBlock{...}}` | `bedrocklight.Block{Text: "..."}` |

We **deliberately don't** support SSO login, STS AssumeRole chains, MFA, IMDS,
ECS, or web identity. Those are the painful, security-sensitive flows; users
delegate them to the AWS CLI or `assume`, and we just consume the resulting
credentials.

## What's in scope

Anything required to call `ConverseStream` with credentials acquired through:

- **Environment variables**: `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`,
  `AWS_SESSION_TOKEN`.
- **`~/.aws/credentials`** static profiles (long-term keys or `assume`-written
  short-term keys).
- **`credential_process`** (executes a command, parses JSON from stdout).
- **Region** from env or `~/.aws/config`.
- **Endpoint** override via env or option (FIPS, LocalStack, custom VPC).

Wire concerns:

- SigV4 signing of bounded JSON request bodies.
- Decoding `application/vnd.amazon.eventstream` framing in responses.
- Retry with exponential backoff on 429/5xx, honoring `Retry-After`.

## What's out of scope (do NOT add without discussion)

- SSO login flow / token cache / OIDC refresh — use `aws sso login` upstream.
- STS `AssumeRole` / `source_profile` chains — `assume` resolves these.
- MFA prompts (`mfa_serial`).
- IMDS / ECS task creds / web identity (IRSA).
- Other Bedrock APIs (`InvokeModel`, non-stream `Converse`).
- Presigned URLs, query-string auth.
- Streaming-payload signing (`STREAMING-AWS4-HMAC-SHA256-PAYLOAD`).
- Adaptive retry, request compression, chunked transfer encoding.

If a real user need surfaces for any of the above, **open an issue first** so
we can decide whether the cost (LOC, complexity, security surface) is worth
the value before any code is written.

## Architecture

```
bedrocklight/                 root package — public API
├── types.go                  Message, Block, Tool, ToolChoice, ConverseStreamInput
├── request.go                JSON marshalling + buildRequestBody
├── client.go                 Client, ConverseStream, Stream, Event*
│
├── awsini/                   AWS shared-ini parser (config + credentials)
├── creds/                    credential resolver (env, profile, credential_process)
├── sigv4/                    AWS Signature Version 4 signer
├── eventstream/              AWS event-stream frame decoder + encoder
│
└── e2e/                      separate go.mod — AWS SDK pulled only here
    ├── sigv4_test.go         byte-equal Authorization vs aws-sdk-go-v2 v4 signer
    └── eventstream_test.go   round-trip through aws-sdk-go-v2 eventstream
```

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

### 100% test coverage is the gate

`make test` runs `go test -race -shuffle=on -cover ./...` and **fails the build
if coverage is below 100%** (excluding paths in `.covignore`). This is
non-negotiable — the whole library is small enough that 100% is achievable and
gives us confidence to keep dependencies at zero.

Mechanics:
- `coverage.tmp.out` is the raw profile.
- `.covignore` is a list of regexes (one per line, passed to `grep -v -E -f`)
  excluding paths from the gate. Today: only the `e2e/` module is excluded
  (it lives in its own go.mod and isn't part of the main coverage profile
  anyway, but the line is there as a guard if files ever move).
- `coverage.out` is the filtered profile that the 100% check runs against.
- Add a path to `.covignore` only when there's a concrete reason it can't be
  unit-tested (e.g. it's a separate go.mod / e2e / hand-verified vendor
  fixture). Discuss before adding new entries.

If a line is genuinely impossible to cover (truly dead code), **delete the
line** rather than ignoring it. We had several "defensive" checks that the
type system or earlier validation already guaranteed; we removed them.

### Zero runtime dependencies

`go.mod` must list **no `require` block** beyond Go stdlib.
`go test ./...` must work with **no module download**.
If you find yourself wanting an external dep, the answer is "write 30 LOC
instead" — every module we ship has a stdlib equivalent that's small enough.

The `e2e` module's `go.mod` is the only exception, and it's intentional.

### Idiomatic Go, simple code

- Small, focused packages.
- Custom JSON marshalling lives next to the types it serializes.
- Errors are descriptive (`"bedrocklight: ..."` prefix) and wrap with `%w`.
- No `init()` magic, no global state, no `sync.Mutex` where `sync.Once` or
  `atomic` would do.
- Tests use stdlib `testing` only — no testify.

### Adding new features

Order of operations for any user-visible change:

1. **Justify it**: does it require touching this library, or could the caller
   handle it? (Many things — extra retry policies, custom logging — belong in
   user code, not here.)
2. **Update this `AGENTS.md`** if scope changes.
3. **Write a failing test first** (especially for bug fixes — confirms the
   test catches the bug).
4. **Implement.**
5. **Cover the change to 100%.** Don't add `.covignore` entries.
6. **Cross-check in `e2e/`** if the change touches SigV4 or event-stream
   framing — those are the parts most likely to have subtle bugs that only a
   reference implementation catches.

### Do not

- Add `time.Sleep` to tests; use `WithBackoff(func(int) time.Duration { return 0 })`
  or `WithNow` to control timing deterministically.
- Add a global logger. Take an `io.Writer` or callback option if logging is
  truly needed (it isn't, today).
- Catch panics. The library never panics on caller input; if it would, fix
  the input validation instead.
- Use `interface{}` / `any` in public types where a concrete type fits.
  `Event.Decoded` is `any` because the decoded type depends on `Type` —
  that's the legitimate use.

## Roadmap

These are explicitly **deferred** until a real user need surfaces:

- Cross-check `ConverseStream` end-to-end against the AWS SDK in `e2e/`
  (build the same input, capture both wire payloads, diff). Today we only
  cross-check SigV4 + event-stream framing.
- A few more `Event*` typed payloads as Bedrock adds new content block kinds.
- Optional `httptrace` hooks for debugging.
- Pluggable retry classifier (right now retries are hardcoded on 429/5xx).

## Out of scope, forever (probably)

- Other AWS services. If you need S3, use the SDK.
- Synchronous (non-streaming) `Converse`. fir doesn't need it; if you do,
  fork.
- Any feature that requires an outbound network call to a non-Bedrock
  endpoint (STS, SSO portal, OIDC). Those belong in the credential-acquisition
  step, which we delegate to upstream tools by design.

## Quick reference

```bash
make test         # 100% coverage gate, race detector, shuffled
make test-fast    # cached, no race
make e2e          # cross-check against AWS SDK
make tidy         # go mod tidy in both modules
make open_coverage
```
