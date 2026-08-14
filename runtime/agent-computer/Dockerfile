# The controller selects one of the official multi-architecture OS images at
# build time. Ubuntu 24.04 is the product default; Ubuntu 26.04 and Debian 13
# are explicit alternatives. We deliberately do not install Ubuntu's
# `chromium` package: on Ubuntu it is a Snap transition package and Snap does
# not provide a working desktop browser inside this container. Playwright's
# pinned Chromium binary is installed below instead.
ARG COMPUTER_BASE_IMAGE=ubuntu:24.04
FROM ${COMPUTER_BASE_IMAGE}

ENV DEBIAN_FRONTEND=noninteractive
ENV DISPLAY=:99
ENV PLAYWRIGHT_BROWSERS_PATH=/ms-playwright

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        bash \
        ca-certificates \
        curl \
        dbus-x11 \
        git \
        nodejs \
        npm \
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
    && npx --yes playwright-core install --with-deps chromium \
    && chromium_path="$(find /ms-playwright -type f -path '*/chrome-linux/chrome' -print -quit)" \
    && test -n "${chromium_path}" \
    && ln -s "${chromium_path}" /usr/local/bin/chromium \
    && npm cache clean --force
COPY --chown=agent:agent computer-server.mjs ./computer-server.mjs
COPY --chown=agent:agent entrypoint.sh ./entrypoint.sh
RUN chmod 0755 ./entrypoint.sh \
    && mkdir -p /workspace /home/agent/.cache /home/agent/.chromium-profile \
    && chown -R agent:agent /workspace /home/agent /ms-playwright

# The entrypoint briefly needs root to make bind-mounted workspace/profile
# directories writable in Docker's Linux VM. It drops to `agent` before
# starting Xfce, Chromium, or the computer server.
USER root
WORKDIR /opt/agent-computer

ENTRYPOINT ["/opt/agent-computer/entrypoint.sh"]
