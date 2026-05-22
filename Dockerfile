# Stage 1: Build the statically linked Go binary
FROM golang:1.24-alpine AS builder

# Install git for dependency resolution
RUN apk add --no-cache git

WORKDIR /app

# Cache dependencies
COPY go.mod go.sum ./
RUN go mod download

# Copy source and build
COPY . .
# CGO_ENABLED=0 creates a purely static binary
# -ldflags="-w -s" strips debug info for a smaller image
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -ldflags="-w -s" -o aeroproxy ./cmd/aeroproxy/main.go

# Stage 2: Final minimal runtime image
FROM alpine:3.20

# Install CA certificates for HTTPS proxying
RUN apk add --no-cache ca-certificates

WORKDIR /

# Copy the binary from builder
COPY --from=builder /app/aeroproxy /usr/local/bin/aeroproxy
# Copy default config
COPY config.yaml /config.yaml

# Expose ports: 8080 (Gateway), 9090 (Mgmt/Metrics), 7946 (Gossip TCP/UDP)
EXPOSE 8080 9090 7946/tcp 7946/udp

# Use a non-root user for security
RUN adduser -D aeroproxy
USER aeroproxy

ENTRYPOINT ["aeroproxy"]