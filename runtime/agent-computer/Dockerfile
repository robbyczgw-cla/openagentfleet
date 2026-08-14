# Pin the multi-architecture Node base manifest; update deliberately with the
# digest and the release provenance when the Computer image is upgraded.
FROM node:22-bookworm@sha256:0557ac14e0d45d02ed563067b82856ca5e7aa3437fa28d98d4350ea9c3d9494a

ENV DEBIAN_FRONTEND=noninteractive
ENV DISPLAY=:99

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        bash \
        ca-certificates \
        chromium \
        curl \
        dbus-x11 \
        git \
        python3 \
        scrot \
        xfce4 \
        xfce4-terminal \
        xdotool \
        xvfb \
    && rm -rf /var/lib/apt/lists/* \
    && useradd --create-home --shell /bin/bash agent

WORKDIR /opt/agent-computer
COPY package.json ./package.json
COPY package-lock.json ./package-lock.json
RUN npm ci --omit=dev --ignore-scripts --no-audit --no-fund \
    && npm cache clean --force
COPY --chown=agent:agent computer-server.mjs ./computer-server.mjs
COPY --chown=agent:agent entrypoint.sh ./entrypoint.sh
RUN chmod 0755 ./entrypoint.sh \
    && mkdir -p /workspace /home/agent/.cache /home/agent/.chromium-profile \
    && chown -R agent:agent /workspace /home/agent

USER agent
WORKDIR /opt/agent-computer

ENTRYPOINT ["/opt/agent-computer/entrypoint.sh"]
