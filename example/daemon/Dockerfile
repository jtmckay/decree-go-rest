# `decree daemon` for the project mounted at /project: a starting point.
# The daemon runs your machines' scripts, so this image is yours to fill in
# with whatever they need; this one has only bash, curl, git and CA certificates.

ARG DECREE_TAG=v0.5.0

FROM ubuntu:24.04
ARG DECREE_TAG
RUN apt-get update \
    && apt-get install -y --no-install-recommends bash ca-certificates curl git \
    && rm -rf /var/lib/apt/lists/*
# The release's own install.sh fetches its static Linux x86-64 binary and checks its sha256.
RUN curl -fsSL -o /tmp/install.sh "https://github.com/jtmckay/decree/releases/download/${DECREE_TAG}/install.sh" \
    && XDG_BIN_HOME=/usr/local/bin sh /tmp/install.sh "${DECREE_TAG}" \
    && rm /tmp/install.sh
# Your scripts' tools go here, for example:
# RUN apt-get update && apt-get install -y --no-install-recommends jq && rm -rf /var/lib/apt/lists/*
WORKDIR /project
# The ubuntu user, uid 1000; compose.yml sets the owner of the project.
USER ubuntu
ENTRYPOINT ["decree", "daemon"]
CMD ["--interval", "2s"]
