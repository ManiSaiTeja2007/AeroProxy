FROM golang:1.24-alpine AS builder
WORKDIR /app
COPY main.go ./
RUN CGO_ENABLED=0 GOOS=linux go build -o mock_backend main.go

FROM alpine:3.20
WORKDIR /
COPY --from=builder /app/mock_backend /mock_backend
EXPOSE 8080
ENTRYPOINT ["/mock_backend"]
