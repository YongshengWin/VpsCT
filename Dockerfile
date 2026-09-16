# syntax=docker/dockerfile:1.7
# ---- web ----
FROM node:24-alpine AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json* ./
RUN npm ci --no-audit --no-fund
COPY web/ .
RUN npm run build

# ---- go ----
FROM golang:1.26.8-alpine AS build
ARG VERSION=dev
ARG COMMIT=
WORKDIR /src
RUN apk add --no-cache git
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/web/dist ./web/dist
ENV CGO_ENABLED=0
RUN go build -trimpath -ldflags "-s -w -X ctlvps/internal/buildinfo.Version=${VERSION} -X ctlvps/internal/buildinfo.Commit=${COMMIT}" -o /out/ctlvpsd ./cmd/ctlvpsd \
 && GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-s -w -X ctlvps/internal/buildinfo.Version=${VERSION}" -o /out/agents/ctlvps-agent-linux-amd64 ./cmd/ctlvps-agent \
 && GOOS=linux GOARCH=arm64 go build -trimpath -ldflags "-s -w -X ctlvps/internal/buildinfo.Version=${VERSION}" -o /out/agents/ctlvps-agent-linux-arm64 ./cmd/ctlvps-agent

# ---- runtime ----
FROM alpine:3.23
RUN apk add --no-cache ca-certificates tzdata && adduser -D -u 10001 ctlvps \
 && mkdir /data /keys && chown ctlvps:ctlvps /data /keys && chmod 0700 /data /keys
WORKDIR /app
COPY --from=build /out/ctlvpsd /app/ctlvpsd
COPY --from=build /out/agents /app/agents
COPY LICENSE THIRD_PARTY_NOTICES.md /app/licenses/
COPY third_party/ /app/licenses/third_party/
ENV CTLVPS_LISTEN=:8080 \
    CTLVPS_DATA_DIR=/data \
    CTLVPS_AGENT_BIN_DIR=/app/agents \
    CTLVPS_SECRETS_KEY_FILE=/keys/secrets.key
VOLUME ["/data", "/keys"]
EXPOSE 8080
USER ctlvps
HEALTHCHECK --interval=30s --timeout=3s CMD wget -qO- http://127.0.0.1:8080/healthz >/dev/null || exit 1
ENTRYPOINT ["/app/ctlvpsd"]
