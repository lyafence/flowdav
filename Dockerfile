FROM golang:1.26.3-alpine AS builder

RUN apk --no-cache add git make

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /bin/flowdav ./cmd/flowdav

FROM alpine:3.23
RUN apk --no-cache add ca-certificates curl

RUN addgroup -g 1000 flow && \
    adduser -D -u 1000 -G flow flow

WORKDIR /app
COPY --from=builder /bin/flowdav /usr/local/bin/flowdav
COPY --from=builder /app/configs /app/configs
COPY --from=builder /app/README.md /app/README.md

RUN chown -R flow:flow /app/configs

USER flow

CMD ["flowdav", "--help"]
