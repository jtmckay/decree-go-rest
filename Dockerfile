# decree-go-rest and the decree binary it runs, on Debian slim. CI publishes it
# as ghcr.io/jtmckay/decree-go-rest (.github/workflows/docker.yml).
#
# Mount the project's .decree/ at /project/.decree, read-only, and its inbox/
# read-write over it: the config is .decree/decree-go-rest.yml, and the
# endpoints only ever write messages to inbox/. Arguments follow the -config,
# so `docker run … decree-go-rest -check` checks the mounted config.
# The daemon is off (DECREE_GO_REST_DAEMON=false): it runs your scripts, which
# need their own tools and the project read-write, so run `decree daemon` in a
# container of its own (example/compose.yml).
#
#   docker build -t decree-go-rest .
#   docker build --build-arg DECREE_TAG=v0.5.0-beta.5 --build-arg VERSION=v1.0.0 -t decree-go-rest .

ARG DECREE_TAG=v0.5.0-beta.5

FROM rust:1-slim-bookworm AS decree
ARG DECREE_TAG
# --locked builds against decree's own Cargo.lock, so a tag builds the same binary every time.
RUN cargo install --locked --git https://github.com/jtmckay/decree --tag "${DECREE_TAG}" \
    && strip /usr/local/cargo/bin/decree

FROM golang:1.22-bookworm AS build
# The version `decree-go-rest -version` prints; CI passes the git tag or commit.
ARG VERSION=devel
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/decree-go-rest ./cmd/decree-go-rest

FROM debian:bookworm-slim
ARG DECREE_TAG
LABEL org.opencontainers.image.source="https://github.com/jtmckay/decree-go-rest" \
      org.opencontainers.image.description="HTTP front door of a decree project, with decree ${DECREE_TAG}"
RUN apt-get update \
    && apt-get install -y --no-install-recommends bash ca-certificates \
    && rm -rf /var/lib/apt/lists/*
COPY --from=decree /usr/local/cargo/bin/decree /usr/local/bin/decree
COPY --from=build /out/decree-go-rest /usr/local/bin/decree-go-rest
WORKDIR /project
# Inside a container, listen on every interface; publish the port, or keep it on the
# compose network behind a reverse proxy.
ENV DECREE_GO_REST_LISTEN=0.0.0.0:8801 \
    DECREE_GO_REST_DAEMON=false
EXPOSE 8801
USER 1000:1000
HEALTHCHECK --interval=30s --timeout=5s CMD ["decree-go-rest", "-config", "/project/.decree/decree-go-rest.yml", "-healthcheck"]
ENTRYPOINT ["decree-go-rest", "-config", "/project/.decree/decree-go-rest.yml"]
