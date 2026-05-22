# Stage 1: Build the statically linked Go binary
FROM golang:1.26-alpine AS builder

RUN apk add --no-cache git

WORKDIR /app

# Cache dependencies
COPY go.mod go.sum ./
RUN go mod download

# Copy source and build
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -ldflags="-w -s" -o aeroproxy ./cmd/aeroproxy/main.go

# Stage 2: Run container using minimal alpine image for debugging capability
FROM alpine:3.20

RUN apk add --no-cache ca-certificates

WORKDIR /

# Copy compiled executable and default config file
COPY --from=builder /app/aeroproxy /usr/local/bin/aeroproxy
COPY config.yaml /config.yaml

# Expose proxy gateway, management dashboard, and gossip cluster ports
EXPOSE 8080 9090 7946/tcp 7946/udp

ENTRYPOINT ["aeroproxy"]
