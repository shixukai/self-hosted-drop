FROM node:22.21.1-alpine AS web
WORKDIR /build/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM golang:1.27.2-alpine AS go
WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ cmd/
COPY internal/ internal/
COPY db/ db/
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/drop ./cmd/drop && \
    CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/drop-worker ./cmd/drop-worker

FROM postgres:16-alpine AS app
RUN apk add --no-cache ca-certificates restic su-exec && \
    addgroup -g 10001 drop && adduser -D -H -u 10001 -G drop drop
COPY --from=go /out/drop /usr/local/bin/drop
COPY --from=web /build/web/dist /opt/drop/web
COPY deploy/entrypoint.sh /usr/local/bin/drop-entrypoint
WORKDIR /data
ENTRYPOINT ["drop-entrypoint"]
CMD ["drop", "serve"]

FROM alpine:3.23 AS worker
RUN apk add --no-cache su-exec && addgroup -g 10001 drop && adduser -D -H -u 10001 -G drop drop
COPY --from=go /out/drop-worker /usr/local/bin/drop-worker
COPY deploy/entrypoint.sh /usr/local/bin/drop-entrypoint
WORKDIR /data
ENTRYPOINT ["drop-entrypoint"]
CMD ["drop-worker"]
