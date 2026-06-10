# Cambrian — top-level Make targets.
# Use these from the repo root.

.PHONY: build test e2e e2e-live vet lint tidy clean docker

# Compile the swarm + seed-nodes binaries into ./bin/.
build:
	@mkdir -p bin
	go build -trimpath -o bin/swarm ./cmd/swarm
	go build -trimpath -o bin/seed-nodes ./cmd/seed-nodes

# Run the unit test suite. Integration tests are gated by INTEGRATION=1.
test:
	go test ./...

# Run the chunk-33 devnet validation suite. By default this runs only
# the pure-logic tests in tests/e2e/; pass E2E=1 plus the resource env
# vars (DATABASE_URL, REDIS_URL, ANTHROPIC_API_KEY, HELIUS_DEVNET_URL,
# ALCHEMY_BASE_SEPOLIA_URL, TELEGRAM_*) to fire the integration tests.
# Skipped tests print the env var they need so an operator can see at
# a glance what's still missing.
e2e:
	go test -v ./tests/e2e/...

# Same as `e2e` but with E2E=1 already set — handy in CI lanes that
# have the credentials provisioned. Failures here are real failures;
# the require* helpers will produce a clean skip otherwise.
e2e-live:
	E2E=1 go test -v ./tests/e2e/...

# Static analysis via the standard Go vet checks.
vet:
	go vet ./...

# golangci-lint runs a curated set of linters. Install via:
#   go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest
# If golangci-lint isn't on PATH the target falls back to gofmt + vet.
lint:
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run ./...; \
	else \
		echo "golangci-lint not found, falling back to gofmt + vet"; \
		gofmt -l . | (! grep .); \
		go vet ./...; \
	fi

# Sync go.mod / go.sum after dependency changes.
tidy:
	go mod tidy

# Build the production Docker image.
docker:
	docker build -t cambrian:dev .

# Remove build artifacts.
clean:
	rm -rf bin
