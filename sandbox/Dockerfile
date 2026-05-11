FROM node:26-bookworm

RUN apt-get update && apt-get install -y git gh && rm -rf /var/lib/apt/lists/*

RUN npm install -g @anthropic-ai/claude-code

RUN useradd -m -s /bin/bash sandbox

WORKDIR /workspace
RUN chown sandbox:sandbox /workspace

COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

USER sandbox
ENTRYPOINT ["/entrypoint.sh"]
