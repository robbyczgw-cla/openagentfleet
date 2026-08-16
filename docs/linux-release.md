# Linux release runbook

The Linux desktop alpha is a packaged Tauri app with the same `botd`
controller and Agent Computer contract as macOS. It is not a second product
and it does not start Colima. Docker Engine on the host is the Computer
runtime.

This is a public-alpha packaging path. It is not a claim of store signing,
reproducible distro archives, or production support.

## What a Linux release ships

On GNU/Linux, `scripts/build-linux-release.sh` produces:

| Format | Who it is for |
| --- | --- |
| `.deb` | Debian, Ubuntu and derivatives |
| `.rpm` | Fedora, RHEL-compatible and openSUSE |
| `.AppImage` | Portable install without a package manager |

Each artifact includes the native window, bundled `botd`, Agent Computer MCP,
`uv`/`uvx`, the pinned OpenCode sidecar, and the Agent Computer Docker build
context. The installer does **not** start Docker, Chromium, or a container.

Docker is a **Recommends**, not a hard Depends, because users may already have
`docker-ce`, a vendor engine, or rootless Docker. After install:

```sh
# Debian / Ubuntu
sudo apt install -y docker.io
sudo usermod -aG docker "$USER"
sudo systemctl enable --now docker

# Fedora
sudo dnf install -y docker
sudo usermod -aG docker "$USER"
sudo systemctl enable --now docker
```

Log out and back in so the docker group applies. Opening the app still does
not start the Agent Computer. Computer View, or an approved desktop/browser
task, builds and starts the isolated container on demand.

## Required gates

1. Start from a reviewed commit. Record the exact commit next to the artifacts.
2. Build on the target GNU/Linux architecture (`x86_64` first).
3. Run `go test ./...`, client typecheck, and
   `cargo test --manifest-path client/src-tauri/Cargo.toml --locked`.
4. Build the artifacts with the script below.
5. Run `scripts/verify-linux-release.sh` against the `.deb`, `.rpm` and
   `.AppImage`.
6. Publish checksums with the files. Do not claim notarization or a store
   listing.

## Build

```sh
export OPENAGENTFLEET_OPENCODE_BINARY=/path/to/opencode-1.18.10
./scripts/build-linux-release.sh
./scripts/verify-linux-release.sh dist/linux/OpenAgentFleet_0.1.0_amd64.deb \
  dist/linux/OpenAgentFleet-0.1.0-1.x86_64.rpm \
  dist/linux/OpenAgentFleet_0.1.0_amd64.AppImage
```

`scripts/build-linux-release.sh` uses Tauri only for the `.deb`. The `.rpm`
and `.AppImage` are built from that same payload with `rpmbuild` and
`appimagetool`, because Tauri 2.11's in-process RPM bundler hangs after
printing the bundle path. The sidecar step still requires the exact OpenCode
pin from [OpenCode bundling](opencode-bundling.md). Provider CLIs stay
optional.

## Install

```sh
# Debian / Ubuntu
sudo apt install ./OpenAgentFleet_0.1.0_amd64.deb

# Fedora
sudo dnf install ./OpenAgentFleet-0.1.0-1.x86_64.rpm

# Portable
chmod +x OpenAgentFleet_0.1.0_amd64.AppImage
./OpenAgentFleet_0.1.0_amd64.AppImage
```

Then install/start Docker if Computer View is needed. The first Computer start
builds `openagentfleet-agent-computer:ubuntu-24.04` from the bundled context.

## What this release does not prove

- A logged-in provider, a live Chromium frame, or a physical display.
- Store signing, Flatpak, Snap, or AUR publication.
- That Colima, Docker Desktop or OrbStack are Linux defaults. They remain
  macOS-oriented options in Settings.

Use the [fresh-user smoke checklist](fresh-user-smoke-test.md) on a Linux
desktop for live Computer evidence. Record that the proof was a packaged
Linux app plus host Docker, not Colima.
