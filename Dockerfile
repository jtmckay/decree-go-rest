# decree-go-rest and the decree binary it runs, on Debian slim.
#
# Mount the project at /project: .decree/ and decree-go-rest.yml (project: .).
# The image has bash and CA certificates, not your scripts' tools, so either:
#   - set `daemon: { enabled: false }` and let another container run
#     `decree daemon` on the same .decree/ (the API only queues messages), or
#   - build FROM this image and install what your scripts need.
#
#   docker build -t decree-go-rest .
#   docker build --build-arg DECREE_TAG=v0.5.0-beta.2 -t decree-go-rest .

ARG DECREE_TAG=v0.5.0-beta.2

FROM rust:1-slim-bookworm AS decree
ARG DECREE_TAG
# --locked builds against decree's own Cargo.lock, so a tag builds the same binary every time.
RUN cargo install --locked --git https://github.com/jtmckay/decree --tag "${DECREE_TAG}" \
    && strip /usr/local/cargo/bin/decree

FROM golang:1.22-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/decree-go-rest ./cmd/decree-go-rest

FROM debian:bookworm-slim
RUN apt-get update \
    && apt-get install -y --no-install-recommends bash ca-certificates \
    && rm -rf /var/lib/apt/lists/*
COPY --from=decree /usr/local/cargo/bin/decree /usr/local/bin/decree
COPY --from=build /out/decree-go-rest /usr/local/bin/decree-go-rest
WORKDIR /project
# Inside a container, listen on every interface; publish the port, or keep it on the
# compose network behind a reverse proxy.
ENV DECREE_GO_REST_LISTEN=0.0.0.0:8801
EXPOSE 8801
USER 1000:1000
HEALTHCHECK --interval=30s --timeout=5s CMD ["decree-go-rest", "-config", "/project/decree-go-rest.yml", "-healthcheck"]
ENTRYPOINT ["decree-go-rest"]
CMD ["-config", "/project/decree-go-rest.yml"]
