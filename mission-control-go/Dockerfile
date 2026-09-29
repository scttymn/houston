# Stages: toolchain (Go, templ, sqlc, sqlite3), dev (the mounted code,
# rebuilt on change), test, and production (a static binary on an empty
# image). Houston builds dev for gantry dev, test for gantry test, and
# production to deploy.

FROM golang:1.27-bookworm AS toolchain
COPY --from=sqlc/sqlc:1.31.1 /workspace/sqlc /usr/local/bin/sqlc
# sqlite3, for db console.
RUN apt-get update && apt-get install -y --no-install-recommends sqlite3 && rm -rf /var/lib/apt/lists/*
# nodynamic: gantry's image codecs run as WASM, so the binary stays static.
ENV CGO_ENABLED=0 GOFLAGS="-buildvcs=false -tags=nodynamic"
RUN go install github.com/a-h/templ/cmd/templ@v0.3.1020
WORKDIR /app

# templ regenerates on change and restarts the server. Caches live in the
# mounted folder's .cache/ (ignored, and skipped by the watcher), so
# restarts reuse them.
FROM toolchain AS dev
# The app is mission-control-go/ in its module, which is mounted at /app.
WORKDIR /app/mission-control-go
ENV GANTRY_ENV=development ADDR=:8080 DATA_DIR=/data GOMODCACHE=/app/mission-control-go/.cache/go/mod GOCACHE=/app/mission-control-go/.cache/go/cache
CMD ["templ", "generate", "--watch", "-path", "/app/mission-control-go", "--watch-pattern=(.+\\.go$)|(.+\\.templ$)|(.+\\.js$)|(.+\\.css$)|(.+\\.sql$)", "--cmd=go run ./cmd/mission-control-go"]

FROM toolchain AS test
ENV GANTRY_ENV=test
COPY go.mod go.sum ./
RUN go mod download
COPY . .
WORKDIR /app/mission-control-go
RUN templ generate -path /app/mission-control-go
CMD ["sh", "-c", "go vet ./... && go test ./..."]

FROM toolchain AS build
COPY go.mod go.sum ./
RUN go mod download
COPY . .
WORKDIR /app/mission-control-go
RUN templ generate -path /app/mission-control-go && go build -trimpath -ldflags "-s -w" -tags timetzdata,nodynamic -o /out/app ./cmd/mission-control-go
RUN mkdir -p /out/data

FROM scratch AS production
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=build /out/app /app
COPY --from=build --chown=65532:65532 /out/data /data
USER 65532:65532
ENV ADDR=:8080 DATA_DIR=/data
EXPOSE 8080
ENTRYPOINT ["/app"]
