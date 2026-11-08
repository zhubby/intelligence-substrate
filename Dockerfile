ARG NODE_IMAGE=node:22-alpine
ARG GO_IMAGE=golang:1.27.3-alpine
ARG RUNTIME_IMAGE=debian:trixie-slim
FROM --platform=$BUILDPLATFORM ${NODE_IMAGE} AS web
ARG NPM_REGISTRY=https://registry.npmjs.org
WORKDIR /src/web
COPY web/package*.json ./
RUN npm ci --registry="$NPM_REGISTRY"
COPY web/ ./
RUN npm run build

FROM --platform=$BUILDPLATFORM ${GO_IMAGE} AS go
ARG GOPROXY=https://proxy.golang.org,direct
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
RUN apk add --no-cache git
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ cmd/
COPY internal/ internal/
RUN --mount=type=cache,target=/root/.cache/go-build \
    export CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH}; \
    mkdir -p /out; \
    for command in cmd/*; do \
      go build -trimpath -o "/out/is-$(basename "$command")" "./$command" || exit 1; \
    done

FROM ${RUNTIME_IMAGE}
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates git \
 && rm -rf /var/lib/apt/lists/* \
 && groupadd --gid 10001 substrate && useradd --uid 10001 --gid substrate --create-home substrate \
 && mkdir -p /data /app && chown substrate:substrate /data /app
COPY --from=go /out/ /usr/local/bin/
COPY --from=web /src/web/dist/ /app/web/
COPY blueprints/flutter-go/ /app/blueprints/flutter-go/
COPY scripts/build-reference-client.py scripts/android_bundle.py scripts/reference_preview.py scripts/windows_msix.py scripts/scaffold-reference-platforms.py /app/scripts/
WORKDIR /app
ENV IS_LISTEN=0.0.0.0:8080 IS_DATA_DIR=/data IS_STATIC_DIR=/app/web IS_BLUEPRINT_ROOT=/app
USER 10001:10001
EXPOSE 8080
ENTRYPOINT ["is-platform"]
