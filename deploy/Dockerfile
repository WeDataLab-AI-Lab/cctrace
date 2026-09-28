# syntax=docker/dockerfile:1.7

# Build the dashboard here rather than relying on the caller having run
# scripts/build-web.sh first. cctraced embeds internal/web/dist, which is
# gitignored, so a plain `docker build` used to produce a server with an empty
# dashboard — and the build context can even carry the placeholder that the
# test targets leave behind.
FROM --platform=$BUILDPLATFORM node:22-alpine3.24 AS web
WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM --platform=$BUILDPLATFORM golang:1.26-alpine3.24 AS builder
ARG TARGETOS TARGETARCH
ARG VERSION=dev
# UPDATE_PUBLIC_KEY is the standard-base64 encoding of the 32-byte Ed25519
# public key embedded in every cctrace client. The matching 64-byte private key
# must be supplied as a standard-base64 file through the BuildKit secret
# `update_signing_key`; it is never copied into a layer:
#   docker build --build-arg UPDATE_PUBLIC_KEY="$UPDATE_PUBLIC_KEY" \
#     --secret id=update_signing_key,src=/secure/cctrace-update-private-key \
#     -f deploy/Dockerfile .
ARG UPDATE_PUBLIC_KEY
# `required` (default) keeps the internal build fail-closed: a public key and a
# signing secret are both mandatory. `optional` lets a mirror build without our
# signing key, which nobody outside this repository should hold. A client built
# with an empty public key refuses automatic updates outright (fail-closed in
# update.go), so an unsigned mirror build degrades safely rather than silently.
# Injecting a dummy key instead would turn that refusal into a misleading
# "signature mismatch" — hence the ARG.
ARG UPDATE_SIGNING=required
# Endpoints `cctrace init` offers as prompt defaults. Supplied by deploy/push.sh
# from deploy/local-defaults.env; both stay empty when that file is absent, and
# the client then asks the user for each address.
ARG DEFAULT_SYNC_ENDPOINT
ARG DEFAULT_OTEL_ENDPOINT

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# After COPY . ., so a stale or placeholder dist from the build context is
# replaced by the assets built above.
RUN rm -rf internal/web/dist
COPY --from=web /web/out ./internal/web/dist

# Build cctraced (server)
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -ldflags "-X main.version=${VERSION}" -o /cctraced ./cmd/cctraced

# Cross-compile cctrace (client) for all supported platforms
# Validate UPDATE_SIGNING before acting on it: an unrecognised value must fail
# the build, never fall through to "signing skipped".
RUN case "${UPDATE_SIGNING}" in \
      required|optional) ;; \
      *) echo "UPDATE_SIGNING must be 'required' or 'optional', got: '${UPDATE_SIGNING}'" >&2; exit 1 ;; \
    esac; \
    [ "${UPDATE_SIGNING}" = optional ] || test -n "${UPDATE_PUBLIC_KEY}"
ENV CLIENT_LDFLAGS="-X main.updatePublicKeyBase64=${UPDATE_PUBLIC_KEY} -X main.defaultSyncEndpoint=${DEFAULT_SYNC_ENDPOINT} -X main.defaultOTELEndpoint=${DEFAULT_OTEL_ENDPOINT}"
RUN CGO_ENABLED=0 GOOS=darwin  GOARCH=amd64 go build -ldflags "-X main.version=${VERSION} ${CLIENT_LDFLAGS}" -o /downloads/cctrace-darwin-amd64  ./cmd/cctrace
RUN CGO_ENABLED=0 GOOS=darwin  GOARCH=arm64 go build -ldflags "-X main.version=${VERSION} ${CLIENT_LDFLAGS}" -o /downloads/cctrace-darwin-arm64  ./cmd/cctrace
RUN CGO_ENABLED=0 GOOS=linux   GOARCH=amd64 go build -ldflags "-X main.version=${VERSION} ${CLIENT_LDFLAGS}" -o /downloads/cctrace-linux-amd64   ./cmd/cctrace
RUN CGO_ENABLED=0 GOOS=linux   GOARCH=arm64 go build -ldflags "-X main.version=${VERSION} ${CLIENT_LDFLAGS}" -o /downloads/cctrace-linux-arm64   ./cmd/cctrace
RUN CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags "-X main.version=${VERSION} ${CLIENT_LDFLAGS}" -o /downloads/cctrace-windows-amd64.exe ./cmd/cctrace

# Generate and package a signed manifest beside every update artifact. BuildKit
# secret mounts are ephemeral: the signing key is unavailable to subsequent
# layers and is absent from both the builder and runtime image filesystems.
# BuildKit's `required=` is resolved before any ARG is expanded, so it cannot be
# toggled per build; the enforcement point moves into the script instead. With
# UPDATE_SIGNING=required (the default) a missing or empty secret still fails the
# build via `test -s`.
RUN --mount=type=secret,id=update_signing_key,required=false \
    [ "${UPDATE_SIGNING}" = optional ] || { \
      test -s /run/secrets/update_signing_key && \
      go run ./scripts/sign-update-manifest.go \
        -private-key /run/secrets/update_signing_key \
        -public-key "${UPDATE_PUBLIC_KEY}" \
        -version "${VERSION}" \
        /downloads/cctrace-*; }

# codex CLI for weekly AI reports (CCTRACE_AI_RUNTIME=codex-app-server). Off by
# default: the static binaries are about 290MB against a ~52MB image, so only a
# deployment that turns AI reports on passes CODEX_VERSION. The digests are the
# ones GitHub publishes for rust-v0.154.0's musl archives; another version needs
# all four replaced, and a stale digest fails the build rather than installing an
# unchecked binary.
#
# codex-code-mode-host ships as its own archive and is not optional: gpt-5.6-terra
# runs tools through code mode, which spawns this host. Without it every report
# answers from the conversation alone -- "failed to spawn code-mode host
# /usr/local/bin/codex-code-mode-host" in the container, 0 tool calls on the
# dashboard.
FROM --platform=$BUILDPLATFORM alpine:3.24 AS codex
ARG TARGETARCH
ARG CODEX_VERSION=
ARG CODEX_SHA256_AMD64=d7e18b2597ae8f242f5f31ee9e90deef48dbc9edd634d9868fb6435d08c07f02
ARG CODEX_SHA256_ARM64=583b48df32804213bdcd338c2e5adb06b34340821fa757a726cc0a524fa33c27
ARG CODEX_CODE_MODE_HOST_SHA256_AMD64=a68df7cca23c6da7cde175677df7de61c73a234add1333a1254b86d641af01f7
ARG CODEX_CODE_MODE_HOST_SHA256_ARM64=20aefa302c2022b496e32911bf954a5f76c7fd749c6bdb9fbd711e32b66dcbfa
RUN mkdir -p /out && [ -z "${CODEX_VERSION}" ] || { \
      case "${TARGETARCH}" in \
        amd64) arch=x86_64; sum="${CODEX_SHA256_AMD64}"; hostsum="${CODEX_CODE_MODE_HOST_SHA256_AMD64}" ;; \
        arm64) arch=aarch64; sum="${CODEX_SHA256_ARM64}"; hostsum="${CODEX_CODE_MODE_HOST_SHA256_ARM64}" ;; \
        *) echo "no codex build for TARGETARCH=${TARGETARCH}" >&2; exit 1 ;; \
      esac; \
      base="https://github.com/openai/codex/releases/download/rust-v${CODEX_VERSION}"; \
      name="codex-${arch}-unknown-linux-musl"; \
      host="codex-code-mode-host-${arch}-unknown-linux-musl"; \
      wget -q -O /tmp/codex.tar.gz "${base}/${name}.tar.gz" && \
      echo "${sum}  /tmp/codex.tar.gz" | sha256sum -c - && \
      tar -xzf /tmp/codex.tar.gz -C /tmp && \
      install -m 0755 "/tmp/${name}" /out/codex && \
      wget -q -O /tmp/codex-code-mode-host.tar.gz "${base}/${host}.tar.gz" && \
      echo "${hostsum}  /tmp/codex-code-mode-host.tar.gz" | sha256sum -c - && \
      tar -xzf /tmp/codex-code-mode-host.tar.gz -C /tmp && \
      install -m 0755 "/tmp/${host}" /out/codex-code-mode-host; }

FROM alpine:3.24
RUN apk add --no-cache ca-certificates tzdata
COPY --from=builder /cctraced /usr/local/bin/cctraced
# Empty unless CODEX_VERSION was given.
COPY --from=codex /out/ /usr/local/bin/
COPY --from=builder /downloads /downloads
EXPOSE 4317 4318 8080
ENTRYPOINT ["cctraced"]
