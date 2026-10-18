# Build from the repository root; keep provider CLIs unmodified.
FROM node:24.22.0-bookworm-slim

LABEL org.opencontainers.image.title="ainsel-subscription" \
      org.opencontainers.image.description="Native Codex and Claude Code task runtime" \
      org.opencontainers.image.source="https://github.com/ainsel-project/ainsel"

RUN apt-get update && apt-get install -y --no-install-recommends \
        ca-certificates git \
    && rm -rf /var/lib/apt/lists/*

COPY runtimes/subscription/package*.json /opt/ainsel/subscription/
RUN cd /opt/ainsel/subscription && npm ci --omit=dev --no-audit --no-fund \
    && npm cache clean --force
COPY runtimes/subscription/ /opt/ainsel/subscription/
COPY LICENSE THIRD_PARTY_LICENSES.md /opt/ainsel/
COPY LICENSES/ /opt/ainsel/LICENSES/

# UID 1000 is the upstream node user, matching the operator security context.
ENV HOME=/home/agent \
    CODEX_HOME=/home/agent/.codex \
    CLAUDE_CONFIG_DIR=/home/agent/.claude \
    DISABLE_AUTOUPDATER=1 \
    PATH="/opt/ainsel/subscription/node_modules/.bin:${PATH}"
RUN mkdir -p /workspace /home/agent/.codex /home/agent/.claude \
    && chown -R 1000:1000 /workspace /home/agent
WORKDIR /workspace
USER 1000:1000

# command can be overridden for the persistent interactive login pod.
CMD ["node", "/opt/ainsel/subscription/runner.mjs"]
