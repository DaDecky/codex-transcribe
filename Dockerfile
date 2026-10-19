FROM golang:1.27.1-alpine@sha256:8a5910f31396cd4d89662f56c68b3ae31d374308270a1c3bd96672ee5ed43414 AS build
WORKDIR /src
COPY go.mod ./
COPY *.go accounts.html ./
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/codex-transcribe .

FROM alpine:3.24@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6
RUN apk add --no-cache ca-certificates \
    && addgroup -g 10001 dictate \
    && adduser -D -u 10001 -G dictate dictate \
    && mkdir -p /data/config \
    && chown -R dictate:dictate /data
COPY --from=build /out/codex-transcribe /usr/local/bin/codex-transcribe
COPY LICENSE /usr/share/licenses/codex-transcribe/LICENSE
ENV XDG_CONFIG_HOME=/data/config \
    CODEX_DICTATE_CREDENTIAL_STORE=file
USER 10001:10001
ENTRYPOINT ["codex-transcribe"]
CMD ["-listen", "127.0.0.1:8378"]
