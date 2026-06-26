# syntax=docker/dockerfile:1

FROM golang:1.26-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN GOTOOLCHAIN=auto go mod download
COPY . .

# Build all binaries: api, migrate, and seed
RUN GOTOOLCHAIN=auto CGO_ENABLED=0 go build -o /out/library ./cmd/api
RUN GOTOOLCHAIN=auto CGO_ENABLED=0 go build -o /out/library-migrate ./cmd/migrate
RUN GOTOOLCHAIN=auto CGO_ENABLED=0 go build -o /out/library-seed ./cmd/seed

FROM alpine:3.20
# rclone powers the best-effort remote backup-destination mirror (PVC stays primary).
RUN apk add --no-cache rclone
RUN addgroup -S app && adduser -S app -G app
WORKDIR /app
COPY --from=builder /out/library /usr/local/bin/library
COPY --from=builder /out/library-migrate /usr/local/bin/library-migrate
COPY --from=builder /out/library-seed /usr/local/bin/library-seed
COPY internal/ent/migrate/migrations ./internal/ent/migrate/migrations
COPY media/ ./media/
COPY scripts/entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod +x /usr/local/bin/entrypoint.sh
RUN mkdir -p ./config/certs
USER app
EXPOSE 4010
ENV PORT=4010
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
