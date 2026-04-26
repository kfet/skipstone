# bedrock-light

Minimal, zero-dependency Go client for **Amazon Bedrock `ConverseStream`**.

Stdlib only. No `aws-sdk-go-v2`, no `smithy-go`. Designed for tools (like
[fir](https://github.com/kfet/fir)) that need exactly one Bedrock API and
acquire credentials through external tooling such as
[granted.dev `assume`](https://granted.dev/) or long-term IAM keys.

## What's supported

### Credentials
- Environment variables (`AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, `AWS_SESSION_TOKEN`)
- Static keys from `~/.aws/credentials` profiles
- `credential_process` (executes a command, parses JSON from stdout)
- Profile selection via `AWS_PROFILE`

### Region / endpoint
- `AWS_REGION` / `AWS_DEFAULT_REGION` env
- `region = ...` from `~/.aws/config`
- Endpoint override via `AWS_ENDPOINT_URL_BEDROCK_RUNTIME` or `WithEndpoint`

### API
- **`ConverseStream`** only (the API fir uses)
- Text, image, tool_use, tool_result, reasoning, cache_point content blocks
- Tool config (incl. tool choice)
- Inference config (max tokens, temperature, top-p, stop sequences)
- Streaming events: messageStart, contentBlockStart/Delta/Stop, messageStop, metadata

### Wire
- SigV4 request signing (no presigning, no streaming-payload signing)
- AWS event-stream response decoding (prelude + headers + payload + CRC32)
- Retry with exponential backoff on 429/5xx, honoring `Retry-After`

## What's _not_ supported

- SSO login flow / token cache (use `aws sso login` or `assume`)
- STS `AssumeRole` chains via `source_profile` (Granted resolves these)
- MFA, IMDS, ECS task creds, web identity / IRSA
- Other Bedrock APIs (`InvokeModel`, non-stream `Converse`)

If you need any of those, use the official AWS SDK.

## Dependencies

Runtime: **none** (Go stdlib only).
Tests: stdlib only.
e2e module (separate `go.mod`): pulls the AWS SDK at build time only, to
cross-check SigV4 + event-stream behavior against the reference implementation.

## License

MIT
