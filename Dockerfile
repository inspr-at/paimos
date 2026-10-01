# SPDX-License-Identifier: AGPL-3.0-only
# web/dist is built here and embedded by web/embed.go (-tags webembed), which the UI package owns.

FROM node:24 AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
# prebuild runs the release check, which reads ../scripts and ../version.json
COPY scripts/ /src/scripts/
COPY NOTICE /src/NOTICE
COPY Dockerfile /src/Dockerfile
COPY go.mod /src/go.mod
COPY version.json /src/version.json
RUN npm run build

FROM golang:1.26 AS build
WORKDIR /src
ARG TARGETARCH
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/web/dist ./web/dist
# VERSION is the calendar coordinate without the leading v, injected as
# -X github.com/inspr-at/paimos/internal/version.Version=${VERSION}
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH:-amd64} go build \
    -tags webembed \
    -ldflags "-X github.com/inspr-at/paimos/internal/version.Version=${VERSION}" \
    -o /paimos ./cmd/aeon

FROM alpine:3.24
# Pin the Chromium runtime used for quote receipt evidence. Update it with a
# render parity check and a renderer-version bump, not through floating apk.
RUN apk add --no-cache ca-certificates chromium=152.0.7977.82-r0 \
    && addgroup -S -g 65532 aeon && adduser -S -D -u 65532 -G aeon aeon
# tini reaps Chromium helper processes after each render.
RUN apk add --no-cache tini=0.19.0-r3
COPY --from=build /paimos /paimos
COPY NOTICE /usr/share/doc/aeon/NOTICE
# The runtime UID/GID is a contract with every host: writable file mounts
# must be owned by 65532 (the former distroless nonroot user). Never let it float.
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/sbin/tini", "--", "/paimos", "serve"]
