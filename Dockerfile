# Houston CLI toolchain. Develop and test through compose.yml (service `cli`) and bin/.

FROM golang:1.26-bookworm AS base
COPY --from=docker:29-cli /usr/local/bin/docker /usr/local/bin/docker
COPY --from=docker:29-cli /usr/local/libexec/docker/cli-plugins/ /usr/local/libexec/docker/cli-plugins/
ENV CGO_ENABLED=0

FROM base AS dev
CMD ["bash"]

# Release binaries: bin/release exports this stage into dist/.
FROM base AS release-build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN for os in darwin linux; do for arch in arm64 amd64; do \
      GOOS=$os GOARCH=$arch go build -trimpath \
        -ldflags "-s -w -X github.com/scttymn/houston/internal/cli.version=${VERSION}" \
        -o /dist/houston-$os-$arch ./cmd/houston || exit 1; \
    done; done

FROM scratch AS release
COPY --from=release-build /dist/ /
