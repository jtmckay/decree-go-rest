# `decree daemon` for the project mounted at /project: a starting point.
# The daemon runs your machines' scripts, so this image is yours to fill in
# with whatever they need; this one has only bash, git and CA certificates.

ARG DECREE_TAG=v0.5.0-beta.5

FROM rust:1-slim-bookworm AS decree
ARG DECREE_TAG
RUN cargo install --locked --git https://github.com/jtmckay/decree --tag "${DECREE_TAG}" \
    && strip /usr/local/cargo/bin/decree

FROM ubuntu:24.04
RUN apt-get update \
    && apt-get install -y --no-install-recommends bash ca-certificates git \
    && rm -rf /var/lib/apt/lists/*
# Your scripts' tools go here, for example:
# RUN apt-get update && apt-get install -y --no-install-recommends jq curl && rm -rf /var/lib/apt/lists/*
COPY --from=decree /usr/local/cargo/bin/decree /usr/local/bin/decree
WORKDIR /project
# The ubuntu user, uid 1000; compose.yml sets the owner of the project.
USER ubuntu
ENTRYPOINT ["decree", "daemon"]
CMD ["--interval", "2s"]
