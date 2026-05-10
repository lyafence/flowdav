FROM golang:1.26-alpine AS builder

RUN apk add --no-cache git make

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /bin/flowdav-client ./cmd/client && \
    CGO_ENABLED=0 GOOS=linux go build -o /bin/flowdav-server ./cmd/server

FROM alpine:latest
RUN apk --no-cache add ca-certificates curl

RUN addgroup -g 1000 flow && \
    adduser -D -u 1000 -G flow flow

WORKDIR /app
COPY --from=builder /bin/flowdav-client /usr/local/bin/flowdav-client
COPY --from=builder /bin/flowdav-server /usr/local/bin/flowdav-server
COPY --from=builder /app/configs /app/configs
COPY --from=builder /app/README.md /app/README.md

USER flow

CMD ["sh", "-c", "echo 'flowdav - Usage:'; echo '  Copy and edit example config:'; echo '  cp /app/configs/flowdav_client.json.example /app/configs/config.json'; echo '  docker run --rm -v ./config.json:/app/configs/config.json flowdav flowdav-client -c /app/configs/config.json'; echo '  docker run --rm -v ./config.json:/app/configs/config.json flowdav flowdav-server -c /app/configs/config.json'"]
LABEL maintainer="flowdav Team" \
       description="Lightweight SOCKS5 proxy using WebDAV storage" \
       version="1.0.0"
