// e2e cross-checks skipstone against the official AWS SDK at build time.
//
// This module has its own go.mod so that the AWS SDK is NOT a dependency of
// the main library — only of these tests. Run with: `go test ./...` from
// inside the e2e directory, or `make e2e` from the repo root.
package e2e
