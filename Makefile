.PHONY: all default test test-fast run-tests check fmt fmtcheck vet open_coverage e2e tidy

# Default target: cover everything, fast.
# Runs gofmt + vet across both modules, unit tests with 100% coverage gate
# (cached, no race), and the e2e cross-check against aws-sdk-go-v2.
# Use `make test` for the strict pass with -race -shuffle=on (what CI runs).
default: test-fast e2e
	@echo "✓ all green"

all: default

# check runs the static gates (gofmt + go vet) across both modules.
check: fmtcheck vet

# fmt rewrites all Go files in both modules to canonical gofmt style.
# (gofmt walks the file tree; it doesn't care about module boundaries, so
# a single invocation covers e2e/ as well.)
fmt:
	@gofmt -w .

# fmtcheck fails if any Go file is not gofmt-clean.
fmtcheck:
	@out="$$(gofmt -l .)"; \
	if [ -n "$$out" ]; then \
		echo "ERROR: gofmt offenders (run 'make fmt'):"; \
		echo "$$out"; \
		exit 1; \
	fi
	@echo "✓ gofmt clean"

# vet runs `go vet` across both modules. Unlike gofmt, `go vet ./...`
# respects module boundaries, so e2e/ needs its own pass.
vet:
	@go vet ./...
	@cd e2e && go vet ./...
	@echo "✓ go vet clean"

# Run unit tests with 100% coverage gate (excluding paths in .covignore).
# Usage: make run-tests TEST_FLAGS="-race -shuffle=on"
run-tests: check
	@tmpfile=$$(mktemp); \
	trap 'rm -f $$tmpfile' EXIT; \
	go test -cover $(TEST_FLAGS) ./... -coverprofile=coverage.tmp.out > $$tmpfile 2>&1; \
	if [ $$? -ne 0 ]; then \
		cat $$tmpfile; \
		exit 1; \
	fi
	@grep -v -E -f .covignore coverage.tmp.out > coverage.out
	@if go tool cover -func=coverage.out | tail -1 | grep -v '100.0%'; then \
		echo "ERROR: coverage is not 100% — see coverage.out (make open_coverage)"; \
		go tool cover -func=coverage.out | grep -v '100.0%' || true; \
		exit 1; \
	fi
	@echo "✓ coverage 100%"

# Strict test pass: clean cache, race detector, shuffled. This is what CI runs.
test:
	@go clean -testcache
	@GOGC=off $(MAKE) run-tests TEST_FLAGS="-race -shuffle=on"

# Fast test pass: cached, no race.
test-fast:
	@$(MAKE) run-tests

open_coverage:
	go tool cover -html=coverage.out

e2e:
	cd e2e && go test -count=1 ./...

tidy:
	go mod tidy
	cd e2e && go mod tidy
