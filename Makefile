# Cambrian — top-level Make targets.
# Use these from the repo root.

.PHONY: build test vet lint tidy clean docker

# Compile the swarm binary into ./bin/swarm.
build:
	@mkdir -p bin
	go build -trimpath -o bin/swarm ./cmd/swarm

# Run the unit test suite. Integration tests are gated by INTEGRATION=1.
test:
	go test ./...

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
