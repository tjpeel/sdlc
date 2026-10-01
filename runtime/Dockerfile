FROM node:24-bookworm

ARG CODEX_VERSION=0.159.2
ARG GH_VERSION=2.81.0
ARG SKILLS_REVISION=21801216eea73e9b75a2a6ac23e0df62b3cd6b8a
ARG AGENTS_REVISION=1965dbdea022f2c7bd2a7cec159d982dde59ae3e

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates curl git gosu jq openssh-client openssh-server python3 ripgrep \
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
RUN npm install -g "@openai/codex@${CODEX_VERSION}" \
    && codex --version && gh --version

# Keep catalogue sources and generated packages outside the Codex state volume.
# The upstream installers retain symlinks to supporting files in these sources.
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
    bash /opt/sdlc/catalogues/skills/scripts/install-codex-skills \
        --prefix tjpeel /home/node/.agents/skills; \
    bash /opt/sdlc/catalogues/agents/scripts/install-agents \
        --runtime codex --prefix tjpeel /etc/codex/agents

# The bootstrap runs as root to initialise private tmpfs and named volumes.
# Codex and the project tools run as the existing non-root node user.
RUN mkdir -p /workspace /home/node/.codex /home/node/.t3 /run/sdlc /run/sshd \
    && chown node:node /workspace /home/node/.codex /home/node/.t3 \
    && passwd -d node
COPY entrypoint.py /usr/local/bin/sdlc-entrypoint
COPY bin/ /usr/local/bin/
COPY sshd_config /etc/ssh/sshd_config.sdlc
RUN chmod 0755 /usr/local/bin/sdlc-* /usr/local/bin/ssh-sign-file \
    && printf '%s\n' 'if [ -r /run/sdlc/environment ]; then . /run/sdlc/environment; fi' \
       > /etc/profile.d/sdlc.sh

ENV HOME=/home/node CODEX_HOME=/home/node/.codex
WORKDIR /workspace
ENTRYPOINT ["/usr/local/bin/sdlc-entrypoint"]
CMD ["bash", "-l"]
