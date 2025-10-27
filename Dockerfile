FROM golang:1.21-alpine AS builder

WORKDIR /app

# Copy go mod files
COPY go.mod go.sum ./
RUN go mod download
# Copy source code
COPY src/ ./src/
COPY proto/ ./proto/

# Build the application
RUN CGO_ENABLED=0 GOOS=linux go build -o raft-kv-store ./src/main.go

# Final stage
FROM alpine:latest

RUN apk --no-cache add ca-certificates

WORKDIR /app

COPY --from=builder /app/raft-kv-store .

EXPOSE 8080 9090 9091

ENTRYPOINT ["/app/raft-kv-store"]
