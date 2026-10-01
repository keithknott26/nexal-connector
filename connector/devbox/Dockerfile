# syntax=docker/dockerfile:1.7
#
# neXal devbox: the default dev container image (template "devbox" in
# connector/internal/sandbox/devpod.go, used when the user picks no repository,
# template or devcontainer.json). A slim Debian with what it takes to run code:
# git, curl, build tools, Go, Python 3 (pip, venv) and Node LTS, plus the
# openssh-server package the connector's in-container sshd setup (DevSSHDScript)
# uses, so it never has to apt-get install it at start.
#
#   docker buildx build --platform linux/amd64,linux/arm64 -t nexal-devbox:dev connector/devbox
#
# Go and Node come from their official images (same Debian release, glibc), so
# no download or checksum handling is needed here and the versions are pinned by
# tag. Resource limits are NOT set here: the connector applies the instance size
# (--cpus/--memory/--pids-limit) when it starts the container.

# TODO(digest-pin): pin the three images by digest (name:tag@sha256:...). The
# devbox-image workflow prints the resolved digests of every build ("Base image
# digests" step); copy them here.
ARG GO_IMAGE=golang:1.26.8-bookworm
ARG NODE_IMAGE=node:24-bookworm-slim
ARG RUNTIME_IMAGE=debian:bookworm-slim

FROM ${GO_IMAGE} AS go
FROM ${NODE_IMAGE} AS node

FROM ${RUNTIME_IMAGE}
ARG DEVBOX_VERSION=dev
LABEL org.opencontainers.image.title="nexal-devbox" \
      org.opencontainers.image.description="neXal default dev container: git, build tools, Go, Python 3, Node LTS" \
      org.opencontainers.image.source="https://github.com/keithknott26/nexal-connector" \
      org.opencontainers.image.version="${DEVBOX_VERSION}" \
      devcontainer.metadata="[{\"remoteUser\":\"dev\"}]"

RUN export DEBIAN_FRONTEND=noninteractive && apt-get update && apt-get install -y --no-install-recommends \
        ca-certificates curl git openssh-client openssh-server sudo less procps \
        build-essential pkg-config \
        python3 python3-pip python3-venv python3-dev \
    && rm -rf /var/lib/apt/lists/* /var/cache/apt/archives/*.deb \
    # No shared host keys baked into the image: the connector generates its own
    # per-instance key in /var/lib/nexal-ssh.
    && rm -f /etc/ssh/ssh_host_*

COPY --from=go /usr/local/go /usr/local/go
COPY --from=node /usr/local/bin/node /usr/local/bin/node
COPY --from=node /usr/local/lib/node_modules /usr/local/lib/node_modules
RUN ln -s ../lib/node_modules/npm/bin/npm-cli.js /usr/local/bin/npm \
    && ln -s ../lib/node_modules/npm/bin/npx-cli.js /usr/local/bin/npx \
    && ln -s ../lib/node_modules/corepack/dist/corepack.js /usr/local/bin/corepack \
    && ln -s /usr/bin/python3 /usr/local/bin/python

# The workspace user (uid 1000, passwordless sudo). The connector's SSH login
# "nexal" maps onto it; it must not itself be called nexal.
RUN groupadd -g 1000 dev && useradd -m -u 1000 -g 1000 -s /bin/bash dev \
    && printf 'dev ALL=(ALL) NOPASSWD:ALL\n' >/etc/sudoers.d/dev && chmod 0440 /etc/sudoers.d/dev

ENV GOPATH=/home/dev/go \
    PATH=/home/dev/go/bin:/usr/local/go/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin \
    GOTOOLCHAIN=local

RUN go version && node --version && npm --version && python3 --version && python3 -m venv /tmp/v && rm -rf /tmp/v \
    && command -v sshd ssh-keygen git gcc make >/dev/null \
    && printf 'DEVBOX_VERSION=%s\n' "${DEVBOX_VERSION}" >/etc/nexal-devbox-release

# The container itself runs as root (DevPod and the connector exec setup steps as
# root); sessions use remoteUser "dev" from the devcontainer.metadata label.
WORKDIR /home/dev
CMD ["sleep", "infinity"]
