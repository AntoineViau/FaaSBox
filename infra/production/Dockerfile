# ---- Go build stage ----
FROM golang:1.25-alpine AS builder

RUN apk add --no-cache git

WORKDIR /build

COPY server/go.mod server/go.sum ./server/
RUN cd server && go mod download

COPY server/ ./server/
RUN cd server && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /faasbox .

# ---- Angular build stage ----
FROM node:22-alpine AS ng-builder

WORKDIR /ui

COPY ui/package.json ui/package-lock.json ./
RUN npm ci

COPY ui/ ./
RUN npx ng build --configuration=production

# ---- Runtime stage ----
FROM oven/bun:1.3.10-alpine

RUN apk add --no-cache ca-certificates tini wget

ADD https://github.com/benbjohnson/litestream/releases/download/v0.5.9/litestream-0.5.9-linux-x86_64.tar.gz /tmp/litestream.tar.gz
RUN tar -xzf /tmp/litestream.tar.gz -C /usr/local/bin && rm /tmp/litestream.tar.gz

WORKDIR /app

COPY --from=builder /faasbox /app/faasbox
COPY --from=ng-builder /data/pb_public /app/data/pb_public
RUN mkdir -p /app/functions /app/data/pb_data
COPY infra/production/litestream.yml /etc/litestream.yml
COPY infra/production/entrypoint.sh /app/entrypoint.sh

RUN addgroup -S faasbox && adduser -S faasbox -G faasbox \
 && chown -R faasbox:faasbox /app

USER faasbox

EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=5s --start-period=15s --retries=3 \
  CMD wget -qO- http://localhost:8080/health || exit 1

ENTRYPOINT ["tini", "--"]
CMD ["/app/entrypoint.sh"]
