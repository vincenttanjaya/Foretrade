# syntax=docker/dockerfile:1
# One image: the Go API serving both SPAs. PORT 8080 is the web app and its
# API; ADMIN_PORT 8081 the admin console (publish it on 127.0.0.1 only).
# Build stages run on the build host's platform and cross-compile, so a
# multi-arch build needs no emulation.

FROM --platform=$BUILDPLATFORM node:20.20.2-alpine AS spa
WORKDIR /src
COPY web/package.json web/package-lock.json web/
COPY admin/package.json admin/package-lock.json admin/
RUN --mount=type=cache,target=/root/.npm npm ci --prefix web --no-audit --no-fund && npm ci --prefix admin --no-audit --no-fund
COPY web web
COPY admin admin
# The console's "open the app" link is baked in at build time.
ARG APP_URL=http://localhost:8080
RUN cd web && VITE_API_URL=/ npm run build
RUN cd admin && VITE_API_URL=/ VITE_APP_URL="$APP_URL" npm run build

FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS api
WORKDIR /src
COPY api/go.mod api/go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY api .
ARG TARGETOS TARGETARCH COMMIT
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -buildvcs=false -ldflags "-s -w -X main.commit=$COMMIT" -o /out/api ./cmd/api

FROM gcr.io/distroless/static-debian13:nonroot
LABEL org.opencontainers.image.source=https://github.com/vincenttanjaya/Foretrade \
      org.opencontainers.image.description="Self-hosted AI paper-trading desk" \
      org.opencontainers.image.licenses=MIT
COPY --from=api /out/api /api
COPY --from=spa /src/web/dist /web
COPY --from=spa /src/admin/dist /admin
ENV PORT=8080 ADMIN_PORT=8081 WEB_DIR=/web ADMIN_DIR=/admin
EXPOSE 8080 8081
HEALTHCHECK --interval=10s --timeout=5s --start-period=60s --retries=3 CMD ["/api", "healthcheck"]
ENTRYPOINT ["/api"]
