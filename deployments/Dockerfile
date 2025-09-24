# Multi-stage build for transaction indexer
FROM golang:1.25-alpine AS builder

# Set working directory
WORKDIR /app

# Install build dependencies
RUN apk add --no-cache git ca-certificates

# Copy go mod files
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy source code
COPY . .

# Build the binary
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
    -ldflags='-w -s -extldflags "-static"' \
    -a -installsuffix cgo \
    -o indexer cmd/indexer/main.go

# Runtime stage using distroless
FROM gcr.io/distroless/static:nonroot

# Use non-root user (65532:65532 is nonroot user in distroless)
USER 65532:65532

# Set working directory
WORKDIR /app

# Copy binary from builder stage
COPY --from=builder /app/indexer /app/indexer

# Create logs directory (distroless doesn't have mkdir, so we rely on volume mounting)
# The logs directory will be created by the volume mount

# Expose port for health checks and monitoring
EXPOSE 8080

# Environment variables for chains (can be overridden)
ENV CHAINS=tron_testnet
ENV DEBUG=true
ENV CATCHUP=true
ENV MANUAL=true

# Default command
ENTRYPOINT ["/app/indexer"]
CMD ["index", "--chains=${CHAINS}", "--debug=${DEBUG}", "--catchup=${CATCHUP}", "--manual=${MANUAL}"]
