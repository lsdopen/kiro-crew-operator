# The crew gateway image: upstream's own image plus the runtimes an agent needs.
#
# Upstream ships a deliberately minimal image — ca-certificates, curl, git,
# ripgrep, tini, unzip, kiro-cli and the KiroCrew wheel, on python:3.12. That is
# enough to run the gateway but not enough to run the MCP ecosystem, which is
# distributed almost entirely as `uvx` (Python) and `npx` (Node) commands. On the
# stock image a crew's MCP servers simply fail to start, which makes the
# operator's own spec.mcpConfigRef field close to decorative.
#
# So this image adds uv/uvx, node/npm/npx, and the tailscale CLI. Everything is
# copied from an official, digest-pinned image rather than fetched at build time,
# so a build is reproducible and an upgrade is a visible commit here rather than
# a silent drift under us.
#
# EVERY addition is verified at the end of the build. These paths come from the
# upstream images' own layouts, and a layout change would otherwise ship a
# subtly broken image; failing the build is the cheaper outcome.

# Pinned by digest, resolved from ghcr.io/kirodotdev/kirocrew:stable.
# To upgrade: re-resolve the tag's digest and change it here in its own commit.
ARG BASE_DIGEST=sha256:ba01bb1c75ea454af2b53773f39c3b38899f0d1d9ab51a0eb97001df6db27407

# uv publishes its binaries at the image root (its own ENTRYPOINT is /uv).
FROM ghcr.io/astral-sh/uv@sha256:b485bd65cc2cf1c9a93b3554012c9c3778cf7b1b5fd3d3096ce9e1226c97e1e6 AS uv

# Tailscale's image is Alpine-based while ours is Debian, which is fine because
# these are static Go binaries — the version check at the end of the build is
# what actually proves it rather than assuming.
FROM ghcr.io/tailscale/tailscale@sha256:8c42c4574ab066384fcb72f69e086a2ff1dd3652eb6f56856cee34bcf0d2f680 AS tailscale

# trixie-slim matches the base image's own Debian release, so the runtime's
# glibc is the one node was built against.
FROM docker.io/library/node:22-trixie-slim@sha256:7b8a0c89c54499bee567618f96578e1a12a800f062fbdbfd1fb6a443fa6f6284 AS node

FROM ghcr.io/kirodotdev/kirocrew@${BASE_DIGEST}

# Upstream ends on USER kirocrew; installing needs root, and the final USER
# below hands it back. ENTRYPOINT, CMD and WORKDIR are inherited untouched.
USER root

# uv and uvx — the runtime for uvx-distributed MCP servers.
COPY --from=uv /uv /uvx /usr/local/bin/

# node and npm — npx-distributed MCP servers, and any JS/TS work a crew does.
# npm is a JS tree rather than a binary, so its whole node_modules comes along
# and the bin entries are re-linked below.
COPY --from=node /usr/local/bin/node /usr/local/bin/node
COPY --from=node /usr/local/lib/node_modules /usr/local/lib/node_modules

# The tailscale CLI only. tailscaled stays in the sidecar: KiroCrew's entrypoint
# owns PID 1 here, so this container cannot supervise a second daemon. The CLI
# earns its place by making the gateway's own tailnet status and serve paths work
# instead of reporting "Tailscale is not installed here" — the product resolves
# it from a fixed allowlist of absolute paths and never consults PATH, and
# /usr/local/bin/tailscale is on that list.
COPY --from=tailscale /usr/local/bin/tailscale /usr/local/bin/tailscale

RUN set -eux; \
    ln -sf ../lib/node_modules/npm/bin/npm-cli.js /usr/local/bin/npm; \
    ln -sf ../lib/node_modules/npm/bin/npx-cli.js /usr/local/bin/npx; \
    # Fail the build now rather than ship an image whose tools are missing or
    # cannot execute on this libc.
    uv --version; \
    uvx --version; \
    node --version; \
    npm --version; \
    npx --version; \
    tailscale version --daemon=false; \
    # The agent runtime and the gateway must still be intact.
    command -v kiro-cli; \
    command -v kirocrew

USER kirocrew

LABEL org.opencontainers.image.title="kiro-crew-gateway" \
      org.opencontainers.image.description="KiroCrew gateway with uv, Node and the Tailscale CLI, for the kiro-crew-operator" \
      org.opencontainers.image.source="https://github.com/lsdopen/kiro-crew-operator" \
      org.opencontainers.image.base.digest="${BASE_DIGEST}" \
      org.opencontainers.image.base.name="ghcr.io/kirodotdev/kirocrew"
