# syntax=docker/dockerfile:1
# Multi-stage build: compile statically in a Go builder image, then copy the
# single binary into a scratch image. Result: a minimal runtime container
# with no shell, no package manager, and a tiny attack surface.

FROM golang:1.25-alpine AS builder
WORKDIR /src

# Cache module downloads in a separate layer for fast incremental builds.
COPY go.mod go.sum* ./
RUN go mod download || true

COPY . .

# CGO_ENABLED=0 produces fully static binaries that run in scratch.
# Both binaries the operator needs at runtime: swarm boots every subsystem;
# seed-nodes materializes the initial gene pool from seed.yaml.
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/swarm ./cmd/swarm
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/seed-nodes ./cmd/seed-nodes

# Final image: scratch + ca-certificates so HTTPS calls to Anthropic, OpenAI,
# Jupiter, 1inch, etc. work out of the box.
FROM scratch
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=builder /out/swarm /swarm
COPY --from=builder /out/seed-nodes /seed-nodes
# seed.yaml.example ships inside the image so operators can `docker cp` it
# out, edit, and pass back via `-v` at runtime.
COPY --from=builder /src/seed.yaml.example /seed.yaml.example

ENTRYPOINT ["/swarm"]
