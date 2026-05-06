# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.1.0] - 2026-05-06

### Added
- `EventContentBlockDelta` now decodes `reasoningContent.redactedContent`
  (encrypted reasoning) and a generic `citation` raw payload.
- `EventMetadata` now decodes `trace`, `performanceConfig`, and `serviceTier`.
- `WithHTTPTrace(func(context.Context) *httptrace.ClientTrace)` — opt-in
  per-request `httptrace` hook for debugging connection / TLS / DNS timings.
  No global state; nothing is logged unless the caller's trace does so.
- `WithRetryClassifier(func(*http.Response, error) bool)` — pluggable retry
  policy. Default behaviour (retry transport errors, 429, and 5xx) is
  preserved when not set.
- Initial public release of `skipstone`: zero-dependency Go client for
  Amazon Bedrock's `ConverseStream` API.
- SigV4 request signing (stdlib only).
- AWS event-stream response decoding (prelude + headers + payload + CRC32).
- Credential providers: environment, shared profile, `credential_process`,
  STS `AssumeRole` chains (with MFA, `external_id`, `source_profile` /
  `credential_source`), web identity / IRSA, ECS task creds, EC2 IMDSv2.
- Region resolution from env or `~/.aws/config`; endpoint override via env
  or `WithEndpoint`.
- Retry with exponential backoff on 429/5xx, honoring `Retry-After`.
- 100% test coverage gate; cross-checks against `aws-sdk-go-v2` for SigV4
  and event-stream framing in a separate `e2e/` module.
- MIT license; GitHub Actions CI on Go 1.21 (floor) and latest stable.

[Unreleased]: https://github.com/kfet/skipstone/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/kfet/skipstone/releases/tag/v0.1.0
