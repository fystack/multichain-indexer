# ============================
# Builder stage
# ============================
FROM golang:1.25-alpine AS builder

# Set working directory
WORKDIR /app

# Install build dependencies (CGO needs gcc, musl-dev)
RUN apk add --no-cache git ca-certificates build-base

# Copy go mod files and download dependencies
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build arguments passed automatically by buildx
ARG TARGETOS
ARG TARGETARCH

# Build static binary
RUN CGO_ENABLED=1 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -ldflags='-w -s -extldflags "-static"' \
    -a -installsuffix cgo \
    -o indexer ./cmd/indexer/main.go


# ============================
# Runtime stage (Distroless)
# ============================
FROM gcr.io/distroless/static:nonroot

# Non-root user (65532:65532 = nonroot in distroless)
USER 65532:65532

WORKDIR /app

COPY --from=builder /app/indexer /app/indexer

EXPOSE 8080

ENV CHAINS=tron_testnet \
    DEBUG=true \
    CATCHUP=true \
    MANUAL=true

ENTRYPOINT ["/app/indexer"]
CMD ["index", "--chains=${CHAINS}", "--debug=${DEBUG}", "--catchup=${CATCHUP}", "--manual=${MANUAL}"]
