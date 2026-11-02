# syntax=docker/dockerfile:1
# openbkn mini: the platform Go services, their stand-ins, the gateway, studio
# and Redis in one image. Build contexts (see compose.yaml):
#   .        this repository
#   foundry  a bkn-foundry checkout (go.mod points at ../bkn-foundry-wt-mini)
#   studio   a bkn-studio checkout

ARG GO_IMAGE=golang:1.26.9
ARG NODE_IMAGE=node:22-alpine
# Redis comes from its official image, which is also the runtime base.
ARG RUNTIME_IMAGE=redis:7

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
    CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/mini ./cmd/mini

############################# runtime ##############################
FROM ${RUNTIME_IMAGE}
# CA roots for calling model providers over HTTPS.
COPY --from=go-build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=go-build /out/mini /app/bin/mini
COPY --from=studio-build /studio/dist /app/studio
COPY dev/run/config /app/run/config
COPY dev/run/profile /app/run/profile
COPY dev/run/authorization-registry.json /app/run/authorization-registry.json
COPY dev/run/models.example.json /app/models.example.json
COPY docker/entrypoint.sh /app/bin/entrypoint.sh
WORKDIR /app/run
ENV TZ=Asia/Shanghai \
    MINI_GATEWAY_ADDR=0.0.0.0:8000 \
    MINI_PUBLIC_URL=http://127.0.0.1:8000 \
    MINI_STUDIO_DIR=/app/studio \
    MINI_OSS_DIR=/data/oss \
    MINI_MODELS_FILE=/data/models.json \
    MINI_FORWARD=127.0.0.1:3306=mariadb:3306,127.0.0.1:9200=opensearch:9200
EXPOSE 8000
VOLUME /data
ENTRYPOINT ["/app/bin/entrypoint.sh"]
CMD []
