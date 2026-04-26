.PHONY: all test test-fast run-tests cover open_coverage e2e tidy

all: test

# Run unit tests with 100% coverage gate (excluding paths in .covignore).
# Usage: make run-tests TEST_FLAGS="-race -shuffle=on"
run-tests:
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

test:
	@go clean -testcache
	@GOGC=off $(MAKE) run-tests TEST_FLAGS="-race -shuffle=on"

test-fast:
	@$(MAKE) run-tests

open_coverage:
	go tool cover -html=coverage.out

e2e:
	cd e2e && go test -count=1 ./...

tidy:
	go mod tidy
	cd e2e && go mod tidy
