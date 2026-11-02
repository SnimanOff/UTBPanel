# syntax=docker/dockerfile:1

FROM node:lts-alpine AS web
WORKDIR /web
COPY frontend/ ./
RUN if [ -f package.json ]; then npm ci && npm run build; \
    else mkdir -p dist && echo '<!doctype html><title>TG Panel</title>' > dist/index.html; fi

FROM golang:alpine AS go
WORKDIR /src/backend
ENV CGO_ENABLED=0 GOFLAGS=-trimpath
COPY backend/go.mod backend/go.sum ./
COPY backend/sdk/go.mod backend/sdk/go.sum ./sdk/
RUN go mod download
COPY backend/ ./
ARG VERSION=dev COMMIT=none DATE=unknown
RUN LDFLAGS="-s -w -X github.com/SnimanOff/UTBPanel/backend/internal/buildinfo.Version=${VERSION} \
      -X github.com/SnimanOff/UTBPanel/backend/internal/buildinfo.Commit=${COMMIT} \
      -X github.com/SnimanOff/UTBPanel/backend/internal/buildinfo.Date=${DATE}" && \
    GOOS=linux GOARCH=amd64 go build -ldflags "$LDFLAGS" -o /out/ ./cmd/edge ./cmd/core ./cmd/panel && \
    GOOS=windows GOARCH=amd64 go build -ldflags "$LDFLAGS -H windowsgui" -o /out/gate/gate.exe ./cmd/gate && \
    GOOS=linux GOARCH=amd64 go build -ldflags "$LDFLAGS" -o /out/gate/gate-linux-amd64 ./cmd/gate && \
    GOOS=darwin GOARCH=arm64 go build -ldflags "$LDFLAGS" -o /out/gate/gate-darwin-arm64 ./cmd/gate && \
    GOOS=darwin GOARCH=amd64 go build -ldflags "$LDFLAGS" -o /out/gate/gate-darwin-amd64 ./cmd/gate

FROM alpine
RUN apk add --no-cache ca-certificates tzdata tini && \
    addgroup -g 1000 tgpanel && \
    adduser -D -H -u 1001 -G tgpanel edge && \
    adduser -D -H -u 1002 -G tgpanel panel && \
    mkdir -p /data/edge /data/core /data/panel /keys /run/tgpanel /srv/gate && \
    chown edge:tgpanel /data/edge && chown panel:tgpanel /data/panel && \
    chmod 0700 /data/edge /data/panel /keys && chmod 0711 /data/core && \
    chown root:tgpanel /run/tgpanel && chmod 2775 /run/tgpanel
COPY --from=go /out/edge /out/core /out/panel /usr/local/bin/
COPY --from=go /out/gate/ /srv/gate/
COPY --from=web /web/dist /srv/web
ENV TGPANEL_SOCKET_GID=1000 TGPANEL_IN_IMAGE=1
ENTRYPOINT ["/sbin/tini", "--"]
