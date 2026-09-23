# Houston CLI toolchain. Develop and test through compose.yml (service `cli`) and bin/.

FROM golang:1.26-bookworm AS base
COPY --from=docker:29-cli /usr/local/bin/docker /usr/local/bin/docker
COPY --from=docker:29-cli /usr/local/libexec/docker/cli-plugins/ /usr/local/libexec/docker/cli-plugins/
ENV CGO_ENABLED=0

FROM base AS dev
CMD ["bash"]
