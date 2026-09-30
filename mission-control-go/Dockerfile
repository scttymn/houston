# Stages: toolchain (Go, templ, sqlc, sqlite3), dev (the mounted code,
# rebuilt on change), test, and production (the static binary, with what
# Mission Control runs). Houston builds dev for gantry dev, test for gantry
# test, and production to deploy.

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
# The assets precompiled as the build does them, so the tests see what
# production serves and no picture is resized on request.
RUN --mount=type=cache,id=mission-control-go-assets,target=/cache/assets,sharing=locked \
    go run ./cmd/mission-control-go assets /cache/assets && cp -a /cache/assets/. assets/built/
CMD ["sh", "-c", "go vet ./... && go test ./..."]

FROM toolchain AS build
COPY go.mod go.sum ./
RUN go mod download
COPY . .
WORKDIR /app/mission-control-go
# The assets precompiled first (Rails' assets:precompile: the scripts
# minified, the pictures' copies), for go build to embed. They're kept in a
# build cache between deploys, so only a new or changed picture is resized.
RUN --mount=type=cache,id=mission-control-go-assets,target=/cache/assets,sharing=locked \
    templ generate -path /app/mission-control-go && go run ./cmd/mission-control-go assets /cache/assets && cp -a /cache/assets/. assets/built/ \
 && go build -trimpath -ldflags "-s -w" -tags timetzdata,nodynamic -o /out/app ./cmd/mission-control-go

# Mission Control drives Docker on its server (restic, volumes, logs), reads
# linked repos with git over ssh, and is its own tools image for helper
# containers (sh, mkdir, and /app backup-sqlite), as the Rails app's image
# was. It runs as 1000, the uid cloudflared reads the tunnel's token as;
# port 80 inside, as the Rails app's, so the tunnel's routes and the
# runners' address stay the same.
FROM debian:bookworm-slim AS production
COPY --from=docker:29-cli /usr/local/bin/docker /usr/local/bin/docker
RUN apt-get update -qq \
 && apt-get install --no-install-recommends -y ca-certificates git openssh-client sqlite3 \
 && rm -rf /var/lib/apt/lists /var/cache/apt/archives \
 && groupadd --system --gid 1000 houston && useradd houston --uid 1000 --gid 1000 --create-home --shell /bin/sh \
 && mkdir /data && chown 1000:1000 /data
COPY --from=build /out/app /app
USER 1000:1000
ENV ADDR=:80 DATA_DIR=/data
EXPOSE 80
# Which Houston this is (HoustonVersion): a release's tag, from the release
# workflow, or a checkout's commit, from the installer. Last, so a new
# version doesn't rebuild the layers above.
ARG HOUSTON_VERSION=""
ARG HOUSTON_SOURCE_SHA=""
ENV HOUSTON_VERSION=$HOUSTON_VERSION \
    HOUSTON_SOURCE_SHA=$HOUSTON_SOURCE_SHA
ENTRYPOINT ["/app"]
