# syntax=docker/dockerfile:1
# openbkn mini: the platform Go services, their stand-ins, the gateway, studio,
# Redis and the official function executor in one image. Build contexts (see
# compose.yaml):
#   .        this repository
#   foundry  a bkn-foundry checkout (go.mod points at ../bkn-foundry-wt-mini)
#   studio   a bkn-studio checkout
# The image also carries ORY Hydra, which runs only with MINI_SAFE=on (the
# platform's own sign-in and permissions instead of the stand-ins).
# MINI_SANDBOX=off builds a much smaller image without the function executor;
# Function and Skill code then does not run.

ARG MINI_SANDBOX=on
ARG GO_IMAGE=golang:1.26.9
ARG NODE_IMAGE=node:22-alpine
# The sandbox runtime base is the platform's sandbox template image: Python
# 3.11, the official function executor, the BKN SDKs and the common data
# packages: the tag built from the foundry commit mini pins (or the latest
# before it). compose.yaml passes the pinned tag; this default matches it.
ARG SANDBOX_IMAGE=ghcr.io/openbkn-ai/sandbox-template-python-basic:0.2.2-main.20261105210745.sha8e69a1a
ARG REDIS_IMAGE=redis:7
# The Hydra release the platform runs (bkn-safe chart), in its SQLite build.
ARG HYDRA_VERSION=26.2.0
ARG HYDRA_SHA256_AMD64=28baf98c6d5da617c905d5ca77826fe863db9f9edab8f72664c69a56bde56b5a
ARG HYDRA_SHA256_ARM64=18e73ac632d2081b76fbceb03130803a63bdb9b8e2ecc3f06f4a6e6820cabe26

############################## studio ##############################
FROM ${NODE_IMAGE} AS studio-build
RUN corepack enable
WORKDIR /studio
COPY --from=studio package.json pnpm-lock.yaml pnpm-workspace.yaml ./
RUN --mount=type=cache,target=/root/.local/share/pnpm/store pnpm install --frozen-lockfile
COPY --from=studio . .
# Real backend, not the mock fixtures.
ENV VITE_USE_MOCK=false
RUN NODE_OPTIONS=--max-old-space-size=4096 pnpm exec vite build

################################ go ################################
FROM ${GO_IMAGE} AS go-build
COPY --from=foundry . /src/bkn-foundry-wt-mini
WORKDIR /src/openbkn-mini
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -tags timetzdata -ldflags "-s -w" -o /out/mini ./cmd/mini

############################## hydra ###############################
# The official release archive, checked against its published checksum.
FROM ${GO_IMAGE} AS hydra
ARG TARGETARCH
ARG HYDRA_VERSION
ARG HYDRA_SHA256_AMD64
ARG HYDRA_SHA256_ARM64
RUN set -eu; \
    case "$TARGETARCH" in \
      amd64) name=64bit; sum=$HYDRA_SHA256_AMD64 ;; \
      arm64) name=arm64; sum=$HYDRA_SHA256_ARM64 ;; \
      *) echo "no Hydra build for $TARGETARCH" >&2; exit 1 ;; \
    esac; \
    file=hydra_${HYDRA_VERSION}-linux_sqlite_${name}.tar.gz; \
    curl -fsSL -o "/tmp/$file" "https://github.com/ory/hydra/releases/download/v${HYDRA_VERSION}/$file"; \
    echo "$sum  /tmp/$file" | sha256sum -c -; \
    mkdir /out; tar -xzf "/tmp/$file" -C /out hydra

############################# runtime ##############################
FROM ${REDIS_IMAGE} AS redis

FROM ${SANDBOX_IMAGE} AS runtime-on

# Redis's own image (Debian slim), plus CA roots for calling model providers.
FROM redis AS runtime-off
COPY --from=go-build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt

FROM runtime-${MINI_SANDBOX}
USER root
COPY --from=redis /usr/local/bin/redis-server /usr/local/bin/redis-server
COPY --from=hydra /out/hydra /usr/local/bin/hydra
COPY --from=go-build /out/mini /opt/mini/bin/mini
COPY --from=studio-build /studio/dist /opt/mini/studio
COPY dev/run/config /opt/mini/run/config
COPY dev/run/profile /opt/mini/run/profile
COPY dev/run/authorization-registry.json /opt/mini/run/authorization-registry.json
COPY dev/run/models.example.json /opt/mini/models.example.json
COPY docker/entrypoint.sh /opt/mini/bin/entrypoint.sh
WORKDIR /opt/mini/run
ENV TZ=Asia/Shanghai \
    MINI_GATEWAY_ADDR=0.0.0.0:8000 \
    MINI_PUBLIC_URL=http://127.0.0.1:8000 \
    MINI_STUDIO_DIR=/opt/mini/studio \
    MINI_OSS_DIR=/data/oss \
    MINI_HYDRA_DIR=/data/hydra \
    MINI_MODELS_FILE=/data/models.json \
    MINI_FORWARD=127.0.0.1:3306=mariadb:3306,127.0.0.1:9200=opensearch:9200 \
    MINI_EXECUTOR_URL=http://127.0.0.1:8080 \
    WORKSPACE_PATH=/workspace
EXPOSE 8000
VOLUME /data
HEALTHCHECK --interval=10s --timeout=3s --start-period=60s --retries=3 \
    CMD ["/opt/mini/bin/mini", "healthcheck"]
ENTRYPOINT ["/opt/mini/bin/entrypoint.sh"]
CMD []
