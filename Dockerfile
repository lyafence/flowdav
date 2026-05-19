FROM golang:1.26.3-alpine AS builder

RUN apk --no-cache add git make

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /bin/flowdav-client ./cmd/client && \
    CGO_ENABLED=0 GOOS=linux go build -o /bin/flowdav-server ./cmd/server && \
    CGO_ENABLED=0 GOOS=linux go build -o /bin/flowdav-encrypt ./cmd/encrypt

FROM alpine:3.23
RUN apk --no-cache add ca-certificates curl

RUN addgroup -g 1000 flow && \
    adduser -D -u 1000 -G flow flow

WORKDIR /app
COPY --from=builder /bin/flowdav-client /usr/local/bin/flowdav-client
COPY --from=builder /bin/flowdav-server /usr/local/bin/flowdav-server
COPY --from=builder /bin/flowdav-encrypt /usr/local/bin/flowdav-encrypt
COPY --from=builder /app/configs /app/configs
COPY --from=builder /app/README.md /app/README.md

RUN chown -R flow:flow /app/configs

USER flow

CMD ["sh", "-c", "echo 'flowdav - Lightweight SOCKS5 proxy over WebDAV'; echo 'Images: https://github.com/lyafence/flowdav/pkgs/container/flowdav'; echo ''; echo 'Usage:'; echo '  docker run --rm -v ./config.json:/app/configs/config.json ghcr.io/lyafence/flowdav flowdav-client -c /app/configs/config.json'; echo '  docker run --rm -v ./config.json:/app/configs/config.json ghcr.io/lyafence/flowdav flowdav-server -c /app/configs/config.json'; echo '  docker run --rm ghcr.io/lyafence/flowdav flowdav-encrypt --gen-keys < config.json > config.enc'; echo ''; echo 'Example config is in /app/configs/flowdav.json.example.'; echo 'See README.md in /app/ for full documentation.'"]
LABEL maintainer="lyafence" \
       description="Lightweight SOCKS5 proxy using WebDAV storage"
