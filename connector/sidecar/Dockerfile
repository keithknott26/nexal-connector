# syntax=docker/dockerfile:1.7
#
# neXal dev-container mesh sidecar: the SAME patched NetBird runtime the Mac and
# the VMs run (v0.79.0 + go-rosenpass v0.5.42 with ML-KEM-1024, profile
# nexal-mlkem1024-tcp-v2), plus the first-boot entrypoint devpod.go expects.
#
# The patch recipe is NOT copied here: it is the repo's own
# experiments/mlkem1024-mesh directory (prepare.py + upstream.json + *.patch),
# passed as the named build context "patches", so there is one source of truth.
#
#   docker buildx build \
#     --build-context patches=experiments/mlkem1024-mesh \
#     --platform linux/amd64,linux/arm64 -t nexal-mesh-sidecar:dev connector/sidecar
#
# prepare.py refuses to patch unless every upstream tree hashes to the value
# pinned in upstream.json, and this file re-checks the NetBird archive hash.

ARG GO_IMAGE=golang:1.26-bookworm
ARG RUNTIME_IMAGE=debian:bookworm-slim

FROM --platform=$BUILDPLATFORM ${GO_IMAGE} AS build
ARG TARGETOS=linux
ARG TARGETARCH
# Keep in step with macos/scripts/update-runtime.sh (same number, "-linux" suffix).
ARG RUNTIME_VERSION=0.79.0-nexal-mlkem1024-gated.9-linux
ENV GOFLAGS= GOWORK=off GOTOOLCHAIN=local CGO_ENABLED=0
RUN apt-get update && apt-get install -y --no-install-recommends patch python3 curl ca-certificates \
    && rm -rf /var/lib/apt/lists/*
COPY --from=patches / /patches
WORKDIR /work
RUN set -eu; \
    manifest() { python3 -c "import json;print(json.load(open('/patches/upstream.json'))$1)"; }; \
    curl -fsSL "https://codeload.github.com/netbirdio/netbird/tar.gz/refs/tags/$(manifest "['netbird']['version']")" -o netbird.tar.gz; \
    echo "$(manifest "['netbird']['archiveSha256']")  netbird.tar.gz" | sha256sum -c -; \
    mkdir pristine && tar -xzf netbird.tar.gz -C pristine
RUN --mount=type=cache,target=/go/pkg/mod set -eu; \
    manifest() { python3 -c "import json;print(json.load(open('/patches/upstream.json'))$1)"; }; \
    moddir() { go mod download -json "$1@$2" | python3 -c 'import json,sys;print(json.load(sys.stdin)["Dir"])'; }; \
    RP=$(cd /tmp && moddir cunicu.li/go-rosenpass "$(manifest "['rosenpass']['version']")"); \
    WG=$(cd /tmp && moddir "$(manifest "['wireguard']['module']")" "$(manifest "['wireguard']['version']")"); \
    python3 /patches/prepare.py --netbird-source "$(ls -d /work/pristine/netbird-*)" \
        --rosenpass-source "$RP" --wireguard-source "$WG" --output /work/candidate
# Tests are run by the macOS runtime build and CI; the image build only compiles.
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build set -eu; \
    cd /work/candidate/netbird; \
    GOOS=$TARGETOS GOARCH=$TARGETARCH go build -mod=mod -trimpath \
        -ldflags="-s -w -X github.com/netbirdio/netbird/version.version=${RUNTIME_VERSION}" \
        -o /out/netbird ./client; \
    grep -qa 'nexal-mlkem1024-tcp-v2' /out/netbird

FROM ${RUNTIME_IMAGE}
ARG RUNTIME_VERSION=0.79.0-nexal-mlkem1024-gated.9-linux
ARG SIDECAR_VERSION=dev
LABEL org.opencontainers.image.title="nexal-mesh-sidecar" \
      org.opencontainers.image.description="neXal dev-container mesh sidecar (patched NetBird, ML-KEM-1024)" \
      org.opencontainers.image.source="https://github.com/keithknott26/nexal-connector" \
      org.opencontainers.image.version="${SIDECAR_VERSION}" \
      systems.nexal.mesh-runtime="${RUNTIME_VERSION}" \
      systems.nexal.mesh-profile="nexal-mlkem1024-tcp-v2"
RUN apt-get update && apt-get install -y --no-install-recommends \
        ca-certificates iproute2 iptables openssh-server \
    && rm -rf /var/lib/apt/lists/* /etc/ssh/ssh_host_* \
    && useradd -m -s /bin/bash nexal && usermod -p '*' nexal \
    && mkdir -p /var/lib/nexal /var/log/nexal /run/sshd
COPY --from=build /out/netbird /usr/local/bin/netbird
COPY entrypoint.sh /usr/local/bin/nexal-sidecar-entrypoint
RUN chmod 0755 /usr/local/bin/nexal-sidecar-entrypoint \
    && printf 'SIDECAR_VERSION=%s\nNETBIRD_RUNTIME_VERSION=%s\nMESH_PROFILE=nexal-mlkem1024-tcp-v2\n' \
        "${SIDECAR_VERSION}" "${RUNTIME_VERSION}" > /etc/nexal-sidecar-release
# Runs as root on purpose: it must create the tun interface and (optionally) run
# sshd, in a network namespace shared with the dev container, with only NET_ADMIN
# added. devpod.go starts it with --cap-add NET_ADMIN --device /dev/net/tun.
VOLUME /var/lib/nexal
ENTRYPOINT ["/usr/local/bin/nexal-sidecar-entrypoint"]
