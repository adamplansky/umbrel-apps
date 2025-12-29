# Build stage
FROM golang:1.25-alpine AS builder

WORKDIR /build

# Install certificates for HTTPS downloads
RUN apk add --no-cache ca-certificates

# Copy source code
COPY main.go .
COPY go.mod .

# Build statically linked binary for smaller image
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o downloader .

# Final stage - minimal image
FROM alpine:3.19

# Install ca-certificates for HTTPS and create non-root user
RUN apk add --no-cache ca-certificates \
    && adduser -D -u 1000 downloader

# Create directories
RUN mkdir -p /data /downloads \
    && chown -R downloader:downloader /data /downloads

WORKDIR /app

# Copy binary from builder
COPY --from=builder /build/downloader .

# Switch to non-root user
USER downloader

# Expose web UI port
EXPOSE 8080

# Store history in /data, downloads in /downloads
CMD ["./downloader", "-web", ":8080", "-o", "/downloads", "-history", "/data/history.json"]
