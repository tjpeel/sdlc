FROM golang:1.27.1-bookworm@sha256:69a7b9788769bec032d238959b61854e9ae87f57be9029ec04e9885fabf99195 AS publisher-build
WORKDIR /src
COPY source/go.mod ./go.mod
COPY source/cmd/ ./cmd/
COPY source/internal/ ./internal/
RUN CGO_ENABLED=0 GOOS=linux GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off \
    go build -trimpath -buildvcs=false -ldflags="-s -w" -o /out/sdlc-publisher ./cmd/sdlc-publisher

FROM docker:29.8.2-cli@sha256:b1805116a6a86cc591b5d5f60a910a0715cdcc9d18d866ad68b1457ead25c35c AS docker-tools

FROM node:24-bookworm@sha256:64af3819f9275802414d7cdc38c27e9d82bd564dec4d4da87d008255d36c63b4

ARG CODEX_VERSION=0.159.3
ARG GH_VERSION=2.102.0
ARG CLAUDE_VERSION=2.1.287
ARG DOTNET_VERSION=10.0.401
ARG SKILLS_REVISION=56e38979baf389b058ae91c6812abae8d8dbcafa
ARG AGENTS_REVISION=347f58e598515c51c42fab4a7f699380db725fd3
# An installation update fills these exact versions in its private build recipe.
# Source builds retain the versions supplied by the pinned Node/Docker images.
ARG NPM_VERSION=
ARG YARN_VERSION=
ARG COMPOSE_VERSION=
ARG BUILDX_VERSION=

RUN apt-get update && DEBIAN_FRONTEND=noninteractive apt-get upgrade -y \
    && apt-get install -y --no-install-recommends \
    ca-certificates curl git gosu jq libicu72 libssl3 libunwind8 \
    openssh-client openssh-server python3 ripgrep zlib1g \
    && rm -rf /var/lib/apt/lists/*
RUN set -eu; \
    case "$(dpkg --print-architecture)" in amd64) arch=amd64 ;; arm64) arch=arm64 ;; *) exit 1 ;; esac; \
    archive="gh_${GH_VERSION}_linux_${arch}.tar.gz"; \
    release="https://github.com/cli/cli/releases/download/v${GH_VERSION}"; \
    curl -fsSL "$release/$archive" -o "/tmp/$archive"; \
    curl -fsSL "$release/gh_${GH_VERSION}_checksums.txt" -o /tmp/gh-checksums.txt; \
    cd /tmp; \
    awk -v name="$archive" '$2 == name { print }' gh-checksums.txt > gh-selected-checksum.txt; \
    test -s gh-selected-checksum.txt; \
    sha256sum -c gh-selected-checksum.txt; \
    tar -xzf "$archive"; \
    install -m 0755 "gh_${GH_VERSION}_linux_${arch}/bin/gh" /usr/local/bin/gh; \
    rm -rf /tmp/gh_*
# npm 12 blocks install scripts by default. Permit only the two pinned clients.
RUN set -eu; \
    if [ -n "$NPM_VERSION" ]; then npm install -g "npm@${NPM_VERSION}"; fi; \
    if [ -n "$YARN_VERSION" ]; then \
        rm -f /usr/local/bin/yarn /usr/local/bin/yarnpkg; \
        npm install -g "yarn@${YARN_VERSION}"; \
    fi; \
    set --; \
    npm_release="$(npm --version)"; \
    if [ "${npm_release%%.*}" -ge 12 ]; then \
        set -- "--allow-scripts=@openai/codex@${CODEX_VERSION},@anthropic-ai/claude-code@${CLAUDE_VERSION}"; \
    fi; \
    npm install -g "$@" "@openai/codex@${CODEX_VERSION}" "@anthropic-ai/claude-code@${CLAUDE_VERSION}" \
    && codex --version && claude --version && gh --version

COPY --from=publisher-build /out/sdlc-publisher /usr/local/bin/sdlc-publisher
COPY --from=docker-tools /usr/local/bin/docker /usr/local/bin/docker
COPY --from=docker-tools /usr/local/libexec/docker/cli-plugins/ /usr/local/libexec/docker/cli-plugins/
# Docker's image can bundle older plugins than their independent releases.
# Installation updates replace only the selected plugins with verified official
# release binaries; ordinary builds keep the pinned image's bundled versions.
RUN set -eu; \
    case "$(dpkg --print-architecture)" in \
        amd64) arch=amd64; compose_arch=x86_64 ;; \
        arm64) arch=arm64; compose_arch=aarch64 ;; \
        *) exit 1 ;; \
    esac; \
    install_plugin() { \
        project="$1"; version="$2"; archive="$3"; target="$4"; \
        [ -n "$version" ] || return 0; \
        release="https://github.com/docker/$project/releases/download/v$version"; \
        mkdir -p /tmp/sdlc-plugin; \
        curl -fsSL "$release/$archive" -o "/tmp/sdlc-plugin/$archive"; \
        curl -fsSL "$release/checksums.txt" -o /tmp/sdlc-plugin/checksums.txt; \
        awk -v name="$archive" '$2 == name || $2 == "*" name { print }' \
            /tmp/sdlc-plugin/checksums.txt > /tmp/sdlc-plugin/selected-checksum.txt; \
        test "$(wc -l < /tmp/sdlc-plugin/selected-checksum.txt)" -eq 1; \
        (cd /tmp/sdlc-plugin && sha256sum -c selected-checksum.txt); \
        install -m 0755 "/tmp/sdlc-plugin/$archive" "/usr/local/libexec/docker/cli-plugins/$target"; \
        rm -rf /tmp/sdlc-plugin; \
    }; \
    install_plugin compose "$COMPOSE_VERSION" "docker-compose-linux-$compose_arch" docker-compose; \
    install_plugin buildx "$BUILDX_VERSION" "buildx-v$BUILDX_VERSION.linux-$arch" docker-buildx
# Microsoft publishes SHA-512 hashes for every SDK archive. Select an exact
# version and architecture and verify before extraction; do not execute an installer.
RUN set -eu; \
    case "$(dpkg --print-architecture)" in amd64) rid=linux-x64 ;; arm64) rid=linux-arm64 ;; *) exit 1 ;; esac; \
    curl -fsSL https://builds.dotnet.microsoft.com/dotnet/release-metadata/10.0/releases.json \
        -o /tmp/dotnet-releases.json; \
    jq -r --arg version "$DOTNET_VERSION" --arg rid "$rid" \
        '[.releases[].sdks[] | select(.version == $version) | .files[] | select(.rid == $rid)][0] | .url, .hash' \
        /tmp/dotnet-releases.json > /tmp/dotnet-archive.txt; \
    url="$(sed -n '1p' /tmp/dotnet-archive.txt)"; \
    hash="$(sed -n '2p' /tmp/dotnet-archive.txt)"; \
    test "$url" != null && test "${#hash}" -eq 128; \
    case "$url" in https://builds.dotnet.microsoft.com/dotnet/Sdk/*) ;; *) exit 1 ;; esac; \
    curl -fsSL "$url" -o /tmp/dotnet-sdk.tar.gz; \
    printf '%s  /tmp/dotnet-sdk.tar.gz\n' "$hash" | sha512sum -c -; \
    mkdir -p /opt/dotnet; \
    tar -xzf /tmp/dotnet-sdk.tar.gz -C /opt/dotnet; \
    rm /tmp/dotnet-releases.json /tmp/dotnet-archive.txt /tmp/dotnet-sdk.tar.gz

# Keep pinned catalogue sources for provenance and generated packages outside
# provider authentication storage.
RUN set -eu; \
    fetch_catalogue() { \
        catalogue="$1"; revision="$2"; \
        test "${#revision}" -eq 40; \
        case "$revision" in *[!0-9a-f]*) exit 1 ;; esac; \
        mkdir -p "/opt/sdlc/catalogues/$catalogue"; \
        curl -fsSL "https://github.com/tjpeel/$catalogue/archive/$revision.tar.gz" \
            -o "/tmp/$catalogue.tar.gz"; \
        tar -xzf "/tmp/$catalogue.tar.gz" --strip-components=1 \
            -C "/opt/sdlc/catalogues/$catalogue"; \
        rm "/tmp/$catalogue.tar.gz"; \
    }; \
    fetch_catalogue skills "$SKILLS_REVISION"; \
    fetch_catalogue agents "$AGENTS_REVISION"; \
    bash /opt/sdlc/catalogues/skills/scripts/install-skills \
        --provider codex --prefix tjpeel -- /home/node/.agents/skills; \
    bash /opt/sdlc/catalogues/agents/scripts/install-agents \
        --runtime codex --prefix tjpeel /etc/codex/agents; \
    bash /opt/sdlc/catalogues/skills/scripts/install-skills \
        --provider claude --prefix tjpeel -- /opt/sdlc/claude/skills; \
    bash /opt/sdlc/catalogues/agents/scripts/install-agents \
        --runtime claude --prefix tjpeel /opt/sdlc/claude/agents

# The agent installer creates owner-only definitions. They contain public
# catalogue instructions and must be owned by the non-root client user.
RUN chown -R node:node /etc/codex/agents /opt/sdlc/claude/agents

# The bootstrap runs as root to initialise private tmpfs and named volumes.
# Codex and the project tools run as the existing non-root node user.
RUN mkdir -p /workspace /home/node/.codex /home/node/.claude /home/node/.t3 /run/sdlc /run/sshd \
    && chown node:node /workspace /home/node/.codex /home/node/.claude /home/node/.t3 \
    && passwd -d node
COPY Dockerfile /opt/sdlc/runtime.Dockerfile
COPY dependencies.py /usr/local/lib/sdlc-dependencies.py
COPY entrypoint.py /usr/local/bin/sdlc-entrypoint
COPY bin/ /usr/local/bin/
COPY sshd_config /etc/ssh/sshd_config.sdlc
RUN chmod 0755 /usr/local/bin/sdlc-* /usr/local/bin/ssh-sign-file \
    && printf '%s\n' 'if [ -r /run/sdlc/environment ]; then . /run/sdlc/environment; fi' \
       > /etc/profile.d/sdlc.sh

ENV HOME=/home/node CODEX_HOME=/home/node/.codex CLAUDE_CONFIG_DIR=/home/node/.claude \
    DOTNET_ROOT=/opt/dotnet PATH=/opt/dotnet:$PATH \
    DOTNET_CLI_TELEMETRY_OPTOUT=1 DOTNET_NOLOGO=1 DISABLE_AUTOUPDATER=1 DISABLE_UPDATES=1
RUN dotnet --version && docker --version && docker compose version \
    && python3 /usr/local/lib/sdlc-dependencies.py capture \
       --skills-revision "$SKILLS_REVISION" --agents-revision "$AGENTS_REVISION"
WORKDIR /workspace
ENTRYPOINT ["/usr/local/bin/sdlc-entrypoint"]
CMD ["bash", "-l"]
