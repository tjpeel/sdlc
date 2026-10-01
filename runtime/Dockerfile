FROM node:24-bookworm

ARG CODEX_VERSION=0.159.2
ARG GH_VERSION=2.81.0

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
