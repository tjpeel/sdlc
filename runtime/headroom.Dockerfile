# Build without a host context: docker build -t sdlc-headroom:0.39.1 - < runtime/headroom.Dockerfile
FROM python:3.12-slim
ENV TIKTOKEN_CACHE_DIR=/opt/tiktoken
RUN pip install --no-cache-dir 'headroom-ai[proxy]==0.39.1' \
    && python -c 'import tiktoken; tiktoken.get_encoding("cl100k_base"); tiktoken.get_encoding("o200k_base")'
LABEL io.sdlc.headroom.version="0.39.1" io.sdlc.headroom.policy="lossless-v1"
USER 1000:1000
ENV HOME=/tmp
ENTRYPOINT ["headroom", "proxy"]
