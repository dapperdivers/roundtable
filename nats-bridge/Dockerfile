FROM golang:1.22-alpine AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /nats-bridge .

FROM alpine:3.19
RUN apk add --no-cache ca-certificates
COPY --from=builder /nats-bridge /usr/local/bin/nats-bridge
EXPOSE 8080
ENTRYPOINT ["nats-bridge"]
