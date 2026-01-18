FROM golang:1.21-alpine AS builder

WORKDIR /build

# Copy go mod files
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build the application
RUN CGO_ENABLED=0 GOOS=linux go build -o app ./example

FROM alpine:latest

# Install wget for healthchecks
RUN apk --no-cache add ca-certificates wget

WORKDIR /app

COPY --from=builder /build/app .

EXPOSE 8080 9090

CMD ["./app"]
