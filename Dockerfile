# ============================
# Builder stage
# ============================
FROM golang:1.25-alpine AS builder

# Set working directory
WORKDIR /app

# Install minimal dependencies
RUN apk add --no-cache git ca-certificates

# Copy go mod files and download deps
COPY go.mod go.sum ./
RUN go mod download

# Copy full source
COPY . .

# Build static binary (Buildx will inject TARGETOS and TARGETARCH)
ARG TARGETOS
ARG TARGETARCH

RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -ldflags='-w -s -extldflags "-static"' \
    -o indexer ./cmd/indexer/main.go


# ============================
# Runtime stage
# ============================
FROM gcr.io/distroless/cc-debian12

WORKDIR /app
COPY --from=builder /app/indexer /app/indexer
USER 65532:65532

EXPOSE 8080

ENTRYPOINT ["/app/indexer"]
CMD ["index"]
