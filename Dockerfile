# syntax=docker/dockerfile:1

# --- UI build -----------------------------------------------------------------------------------------------------
FROM --platform=$BUILDPLATFORM node:26-bookworm-slim AS ui
ENV YARN_ENABLE_SCRIPTS=false YARN_ENABLE_TELEMETRY=0
RUN npm install -g corepack@latest && corepack enable
WORKDIR /src
COPY package.json yarn.lock .yarnrc.yml ./
COPY packages/client/package.json packages/client/
COPY packages/ui/package.json packages/ui/
COPY packages/desktop/package.json packages/desktop/
# Install only what the UI needs (skips Electron).
RUN yarn workspaces focus @v8s/ui
COPY packages/client packages/client
COPY packages/ui packages/ui
RUN yarn workspace @v8s/ui build-only

# --- Agent build (embeds the UI) ---------------------------------------------------------------------------------
FROM --platform=$BUILDPLATFORM golang:1.27.1-bookworm AS agent
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=0.0.0-dev
WORKDIR /src/agent
COPY agent/go.mod agent/go.sum ./
RUN go mod download
COPY agent/ ./
COPY --from=ui /src/packages/ui/dist/ internal/webui/dist/
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath \
    -ldflags "-s -w -X github.com/zyno-io/v8s/agent/internal/version.Version=${VERSION}" \
    -o /out/v8s ./cmd/v8s

# --- Runtime -----------------------------------------------------------------------------------------------------
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=agent /out/v8s /usr/local/bin/v8s
EXPOSE 7931
ENTRYPOINT ["/usr/local/bin/v8s"]
# Hosted multi-user `v8s server` mode (OIDC, per-user state) does not exist yet. Until it lands this runs the bare
# single-user agent: it exposes the credentials the container can reach to anyone who can reach the port and has
# the token printed in its logs. Do not publish the port to an untrusted network. Replace this CMD with `server`
# when hosted mode ships (docs/packaging.md).
CMD ["agent", "--listen", "0.0.0.0:7931"]
