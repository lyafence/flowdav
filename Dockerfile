# syntax=docker/dockerfile:1
FROM --platform=$BUILDPLATFORM golang:1.26.5-alpine AS builder
ARG TARGETOS TARGETARCH VERSION=dev
WORKDIR /app
COPY --link go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download
COPY --link . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /bin/flowdav ./cmd/flowdav

FROM alpine:3.24
RUN apk --no-cache add ca-certificates curl
WORKDIR /app
COPY --link --from=builder /bin/flowdav /usr/local/bin/flowdav
COPY --link configs/flowdav.json.example /app/
USER 1000
CMD ["flowdav", "--help"]
