#!/bin/sh
set -e

# Docker is recommended, not a hard package dependency: users may already have
# docker-ce, a vendor engine, or rootless Docker. The Agent Computer stays
# lazy and is not started by this installer.

echo "OpenAgentFleet is installed. The Agent Computer needs a local Docker Engine."
if command -v apt-get >/dev/null 2>&1; then
  echo "  sudo apt update && sudo apt install -y docker.io"
elif command -v dnf >/dev/null 2>&1; then
  echo "  sudo dnf install -y docker"
elif command -v pacman >/dev/null 2>&1; then
  echo "  sudo pacman -S --needed docker"
fi
echo "Then add your user to the docker group and start a new session:"
echo "  sudo usermod -aG docker \"\$USER\""
echo "  sudo systemctl enable --now docker"
echo "Opening the app does not start a container; Computer View does that on demand."

exit 0
