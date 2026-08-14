# macOS Agent Computer Backends

Research snapshot: 2026-08-13

This document chooses the local runtime for OpenAgentFleet's Agent Computer: a persistent Linux desktop with Chromium, Xfce, a terminal, a file manager, internal Playwright/CDP control, controlled live desktop frames/actions, and an explicit human takeover path. The current native credential path is browser-field password/OTP entry only; CAPTCHA, payment, and desktop credential entry remain manual or unavailable through that path.

The terms **verified** and **inference** are deliberate:

- **Verified** means the cited primary documentation states or demonstrates the capability.
- **Inference** means an OpenAgentFleet design judgment derived from those capabilities.
- **Not documented** means that no official source reviewed here demonstrates the exact GUI workload. It is not a claim that the workload is impossible.

## Decision

OpenAgentFleet should have one runtime-neutral `AgentComputerBackend` contract
and a Docker-compatible backend as the shipped default. The Agent Computer is
**lazy**: opening the app, creating an Agent, or selecting an AI engine never
starts a VM/container. Setup and startup happen only when a user explicitly
opens Computer View or an approved Agent task first needs browser/desktop work.

The Docker-compatible backend should accept both:

1. Docker Desktop's Linux engine, when it is already installed; and
2. Colima's Docker runtime, which is the preferred open-source/macOS distribution path.

This keeps the current implementation useful, avoids making Docker Desktop a hard dependency for an open-source project, and lets users select the runtime that fits their machine. A fresh install should detect an existing Docker endpoint first and otherwise recommend Colima. Docker Desktop remains the compatibility fallback because its macOS installation, Linux VM, image/volume lifecycle, port publishing, and ecosystem are mature and already match the current Agent Computer image.

The runtime selector is now implemented for the Docker-compatible providers:
`auto`, Docker Desktop, Colima, and OrbStack are resolved per `botd` instance
without changing the user's global Docker context. It is a first-use/setup
detail, not an onboarding choice. Apple `container` is inventoried as an
opt-in experimental candidate, but is not selectable as an Agent Computer
backend until its separate adapter passes the same desktop acceptance suite.

## Agent Computer image and resource configuration

The shipped default is **Ubuntu 24.04** (`ubuntu-24.04`). The selector also
offers **Ubuntu 26.04** (`ubuntu-26.04`) and **Debian 13** (`debian-13`). The
choice selects the base image used when the Agent Computer image is built; it
does not change the controller or the browser/desktop view contract.

The standard resource contract is:

| Resource | Default | Optional range |
| --- | ---: | ---: |
| CPU | 4 | 1–16 |
| RAM | 4 GiB | 2–64 GiB |
| Disk | 25 GiB | 10–500 GiB |
| Guest swap | 1 GiB | 0–16 GiB |

These are optional per-computer settings. They apply on the next computer
start, not to an already running instance. Swap is a small emergency buffer,
not a substitute for RAM; increase RAM for sustained memory pressure.

The values have different meanings depending on the Docker-compatible runtime:

- **Colima:** CPU, RAM and disk configure the dedicated Colima VM. The
  requested swap is an app-owned swap file inside the Linux guest. If an
  existing Colima profile already has a larger disk, OpenAgentFleet keeps that
  disk and never attempts to shrink it; a requested 25 GiB therefore does not
  turn an existing 100 GiB profile into a smaller disk.
- **Docker Desktop and OrbStack:** CPU, RAM and swap are passed as per-container
  limits. The disk value does not resize the runtime's VM disk; Docker Desktop
  or OrbStack continue to manage that VM storage in their own settings. The
  host VM may therefore need separate resource configuration even when the
  Agent Computer limits are correct.

Before Colima provisioning, the controller performs a host free-space
preflight for the Colima storage and the Agent Computer workspace/profile. If
the host cannot provide the requested budget, startup stops before the VM or
container is provisioned and returns an explicit, retryable free-space error.
This keeps a full disk from appearing as a generic Docker or Chromium failure.

## First-use product flow

1. The user creates or opens an Agent and chats normally; no computer runtime
   is running.
2. A user request or explicit Computer View action needs browser/desktop work.
3. OpenAgentFleet explains the local isolated-computer boundary, detects an
   existing compatible runtime, and recommends Colima when none is selected.
4. If necessary, it offers an explicit installation command/link, then retry;
   it does not install or switch the user's global Docker context silently.
   On the first Colima computer start, the controller also adds only the
   workspace and controller-owned Chromium-profile directories to the
   dedicated `openagentfleet` profile and restarts that profile only when its
   mount configuration changed.
5. After confirmation, `botd` starts the persistent Chromium/Xfce/Files/
   Terminal environment and makes its controlled view available to that run.

Stopping/resetting/exporting the computer remains explicit. The engine and the
container runtime are independent: choosing Grok Build, Codex App Server, or
OpenCode never implicitly starts a computer.

## Cua Lume and a future macOS Agent Computer

[Lume](https://cua.ai/docs/how-to-guides/lume/install-lume) is relevant, but it
solves a different layer than the current backend. It is Cua's local Apple
Silicon VM manager for macOS and Linux guests. The documented host baseline is
Apple Silicon, macOS 13+, 8 GB RAM (16 GB recommended), and 50 GB free disk.
Lume can create, boot, stop, clone, resize, and expose VMs through its CLI,
HTTP API, or stdio MCP server. The MCP server is a VM-management interface;
it is not by itself our normalized screenshot/click/type/takeover contract.

For a macOS-native Agent Computer, Cua's own guide pairs Lume with **Cua
Driver** inside the guest. That second layer owns desktop capture, input, app
enumeration, and macOS permission/TCC handling. The guide also requires a
versioned macOS image and a sparse VM disk with up to 150 GB reserved for the
driver workflow. This is materially heavier than our current Linux/Xfce/
Chromium computer and requires a first-run display session for Accessibility,
Screen Recording, Automation, and direct-capture consent.

**Decision:** do not make Lume a default or install it silently. Keep Colima +
Docker as the shipped macOS-first backend because it already passes our
Chromium frame, desktop frame, CDP navigation, controlled action, and takeover
contract. Add Lume later as an explicit optional **macOS VM** backend for
native macOS apps, with a pinned guest template and an OpenAgentFleet adapter
that maps Cua Driver into the same `Frame`, `BrowserAction`, `DesktopAction`,
approval, and Teach contracts. Lume/MCP must remain behind our capability and
permission broker rather than being exposed directly to an Agent.

The current development Mac has Lume absent and roughly 104 GiB free on the
system volume. That is enough for the lightweight Colima path, but not a good
place to provision the documented 150 GB Cua Driver VM. This is why Lume is
researched and recorded here, but not yet presented as a working selectable
runtime in the product.

Apple's `container` should be implemented as an experimental backend after the Docker contract is stable. It is now a serious candidate: the official tool consumes OCI images, runs each Linux container in its own lightweight VM, exposes volumes, mounts, ports, `shm-size`, capability controls, and has a new persistent `container machine` feature. However, the official project currently requires Apple silicon and macOS 26, is actively evolving, and does not document an Xfce + Chromium desktop with a controlled remote-input surface. That makes it promising but not yet the default for this product.

Apple `Virtualization.framework` is the long-term high-isolation backend. It is the right escape hatch for a full guest VM and more explicit security boundaries, but it is a new VM product to build: boot images, disk lifecycle, guest agent, networking, desktop streaming, input injection, snapshots, and a Swift/Go integration all become OpenAgentFleet responsibilities.

LXD is not a native macOS daemon choice. Canonical documents the macOS `lxc` client, not the LXD server; the server is installed on Linux. Incus makes the same distinction. LXD/Incus is therefore a remote Linux backend or a later Colima/Incus experiment, not the macOS-first local default.

## Project constraints

The runtime is not the UI. The UI should see a stable Agent Computer with these observable surfaces:

| Surface | Required behavior |
| --- | --- |
| Linux guest | Persistent Linux environment with Xfce, Chromium, terminal, file manager, and a virtual display. |
| Agent browser | Internal Chromium CDP endpoint for Playwright navigation and browser actions; it is not host-published. |
| Agent computer | Desktop actions for click, type, key press, scroll, and screenshots. |
| Human control | An authenticated takeover channel; agent and human input must not be active concurrently. |
| Teaching | Explicitly recorded computer actions, with secret steps paused/redacted and a reviewable skill draft. |
| Persistence | Browser profile and guest state survive normal container recreation; reset/delete must be explicit. |
| Workspace | Only approved folders are mounted. The host home directory, Docker socket, browser cookies, and arbitrary host paths are never implicit. |
| Remote access | The Mac remains the worker. iOS/Android clients later reach the authenticated controller over Tailscale; they do not need a local container runtime. |
| Network | Agent egress and exposed services are policy-controlled. Raw VNC/noVNC is not published. |

OpenAgentFleet's current repository implementation is Docker-specific and already provisions Xvfb/Xfce, Chromium, a controlled desktop-frame/action surface, and a persistent Chromium-profile path in `runtime/agent-computer`. Raw VNC/noVNC is deliberately absent: a client-side read-only flag cannot securely enforce takeover. The current Chromium launch also uses `--no-sandbox` for the Docker Desktop environment. That is a repository observation, not a vendor guarantee; it should remain a backend-specific security debt and an explicit capability/health-check result, not an assumption in the runtime contract.

Playwright's official API supports attaching to an existing Chromium process through `connectOverCDP`; it also warns that CDP attachment is lower fidelity than the Playwright protocol and that the browser must be launched with compatible arguments. See [Playwright `connectOverCDP`](https://playwright.dev/docs/api/class-browsertype). The backend contract therefore uses an internal CDP endpoint but does not make a host-published CDP port a runtime feature.

For a future raw remote-display mode, [noVNC](https://github.com/novnc/noVNC) is a browser VNC client that supports modern desktop and mobile browsers. Its documentation requires WebSockets, either from the VNC server or through [websockify](https://github.com/novnc/websockify). OpenAgentFleet does not currently expose that path: the shipped desktop uses “guest display -> authenticated frame API -> OpenAgentFleet UI” plus a server-gated action API. A future VNC gateway must enforce view/control mode and revocation server-side.

## Runtime findings

### Docker Desktop and the Docker Engine API

**Verified.** Docker Desktop on Mac runs containers behind a Linux VM. Docker documents multiple VMM choices, including the stable Apple Virtualization framework and the newer Docker VMM, which is currently marked Beta and is available on Apple Silicon. Docker's current Mac installation requirements include a supported macOS version and at least 4 GB RAM; Docker supports the current and two previous major macOS releases. See [Install Docker Desktop on Mac](https://docs.docker.com/desktop/setup/install/mac-install/) and [Virtual Machine Manager for Docker Desktop](https://docs.docker.com/desktop/features/vmm/).

**Verified.** Docker named volumes are managed by Docker, survive container deletion, are preferable for persistent/high-performance data, and can be backed up or migrated. Bind mounts directly expose a host path to the container; Docker explicitly warns that processes in the container can modify the host files unless the mount is read-only. On Docker Desktop the daemon is in the Linux VM, with mechanisms that bridge native Mac paths into that VM. See [Docker volumes](https://docs.docker.com/engine/storage/volumes/), [bind mounts](https://docs.docker.com/engine/storage/bind-mounts/), and [Docker Desktop file sharing](https://docs.docker.com/desktop/settings-and-maintenance/settings/).

**Verified.** Published ports are available on the Docker host. Binding a published port to `127.0.0.1` restricts it to the Docker host; publishing without a host address can expose it beyond the host. See [Port publishing and mapping](https://docs.docker.com/engine/network/port-publishing/).

**Verified.** Docker documents a Linux VM boundary: root inside a container is not host root. It also documents Enhanced Container Isolation, which adds user-namespace isolation, but ECI is a Docker Business feature. Host bind mounts still retain the host's permissions. See [Docker Desktop permission requirements and isolation](https://docs.docker.com/desktop/setup/install/mac-permission-requirements/).

**Project inference.** Docker is the lowest-risk first backend because the current image, lifecycle code, and desktop proxy already use the Docker Engine model. Use a named volume for the Chromium profile and guest-private state; use an explicit bind mount only for the approved workspace. Store caches and browser state inside the Linux VM rather than across the Mac/VM file-sharing boundary. Publish the desktop port to loopback and let botd perform the authenticated proxying.

**Trade-offs.** Docker Desktop is not the only Docker-compatible runtime and has licensing/installation policy to consider: Docker's Mac installer states that personal use, education, and non-commercial open source use are free, while larger commercial organizations may need a paid subscription. The open-source project should therefore not silently require Docker Desktop. Docker's Resource Saver can turn off the Linux VM while idle and restart it when containers run; Docker documents a 3–10 second restart window. That is acceptable for a cold-start state, but the UI must show “starting” rather than appear stuck.

### Colima and Lima

**Verified.** Lima launches Linux VMs with automatic file sharing and port forwarding, supports containerd, Docker, Podman, Kubernetes, and non-container Linux workloads, and is Apache-2.0 licensed. See the [Lima project documentation](https://github.com/lima-vm/lima).

**Verified.** Colima supports Intel and Apple Silicon macOS, multiple instances, volume mounts, automatic port forwarding, and Docker, containerd, and Incus runtimes. Its default VM is documented as 2 CPUs, 2 GiB memory, and 100 GiB storage, all configurable. It supports QEMU, Apple's `vz`, and experimental `krunkit` VM types; Rosetta integration is available with `vz` on Apple Silicon/macOS 13+. See the [Colima README](https://github.com/abiosoft/colima) and [Colima's runtime/mount defaults](https://github.com/abiosoft/colima/blob/main/embedded/defaults/colima.yaml).

**Project inference.** Colima is the best open-source-first macOS path for the existing Docker backend. OpenAgentFleet can keep speaking the Docker CLI/API while the user chooses Colima as the Linux VM. This avoids an image rewrite and preserves Docker's mature volume, port, and lifecycle semantics. Lima is the lower-level option when we need a custom Linux VM or containerd path rather than a Docker daemon.

The current local setup has been validated on this Mac with a dedicated
`openagentfleet` profile. The profile is intentionally separate from the
Docker Desktop context:

```text
brew install colima
DATA_DIR="$HOME/Library/Application Support/com.openagentfleet.desktop"
colima --profile openagentfleet start --runtime docker --vm-type vz --cpus 4 --memory 6 --disk 32 \
  --mount "$DATA_DIR:w" --mount "$HOME/Projects/openagentfleet:w" \
  --mount-type virtiofs --activate=false --downloader curl
docker context create colima-openagentfleet --docker "host=unix://$HOME/.colima/openagentfleet/docker.sock"
```

The data-directory mount is required because `botd` bind-mounts the private
Agent workspace from that directory. Chromium state is deliberately a
Docker-managed volume inside the VM; its POSIX `Singleton*` locks must not cross
the macOS virtiofs boundary. If an existing dedicated profile has different
mounts, stop and restart only that profile with the desired mount list before
testing; do not change the global Docker Desktop context.

The Go controller keeps the container service on port `9223` and lets the
host-side proxy port vary with `OPENAGENTFLEET_COMPUTER_PORT` or `-computer-port`.
For example, a daemon using Colima can run with
`DOCKER_CONTEXT=colima-openagentfleet OPENAGENTFLEET_COMPUTER_PORT=19224`; Docker Desktop
can continue using the default host port `9223`. This avoids a false `401`
from one runtime receiving the other runtime's control token.

**Validated in the current run.** Colima passed Xfce/Chromium startup, browser and desktop frames, CDP-backed navigation, controlled actions, and loopback host-port binding through `botd`. Remaining project checks are persistent-profile recovery across a deliberate reset, Tailscale access to `botd`, and sleep/wake recovery. These are project tests, not capabilities proven by Colima's general documentation.

### Apple `container` and Containerization

The capability map, security implications of `container machine`, and adapter
acceptance suite are maintained as internal research. The summary below is the
public decision record used by the runtime selector.

**Verified.** Apple's [Containerization package](https://github.com/apple/containerization) uses `Virtualization.framework` on Apple Silicon, manages OCI images and registries, creates ext4 filesystems, launches lightweight VMs, runs containerized processes, and supports Rosetta for `linux/amd64` containers. Its design runs each Linux container in its own lightweight VM and can allocate a dedicated IP. The package is active development software; its README says source stability is guaranteed only within minor-version ranges.

**Verified.** The official [`container` CLI](https://github.com/apple/container) consumes and produces OCI images, is optimized for Apple Silicon, and currently requires Apple Silicon and macOS 26. The command reference documents `--volume`, `--mount`, `--publish`, `--shm-size`, read-only root filesystems, capability add/drop, `--rosetta`, `exec`, image operations, volume create/list/delete/prune, and user-defined networks on macOS 26+. See the [official command reference](https://github.com/apple/container/blob/main/docs/command-reference.md).

**Verified.** The new [`container machine` feature](https://github.com/apple/container/blob/main/docs/container-machine.md) is explicitly intended for fast, lightweight, persistent Linux environments. It can run an image's init system, register long-running services, keep persistent machine storage, configure CPU/memory, and choose a home mount mode (`rw`, `ro`, or `none`). The current command reference documents machine creation, boot, run, inspect, stop, delete, and resource changes.

**Not documented.** The reviewed Apple sources do not demonstrate an Xfce desktop with Chromium, a controlled frame/action desktop surface, a persistent Chrome profile, or Playwright CDP. OCI ports and mounts make such a build plausible, but “the process can run” is not the same as “the complete Agent Computer works”. We must prototype this before making it a default.

**Project inference.** Apple `container` is the most interesting experimental backend for strong per-container VM boundaries on supported Apple Silicon Macs. `container machine` may be closer to our persistent desktop requirement than a disposable `container run`. It must use `home-mount=none` or a narrowly scoped mount strategy for an agent desktop; the documented default `rw` home mount would be too broad for our threat model.

**Reasons it is not the default yet:** Apple Silicon-only support, macOS 26-only support, active API/CLI evolution, no official GUI-desktop example, and the need to validate Chromium sandboxing, X11 shared memory, input injection, controlled frame delivery, and guest lifecycle. The project should not infer compatibility merely from OCI image compatibility.

### Apple `Virtualization.framework`

**Verified.** Apple's [Virtualization framework](https://developer.apple.com/documentation/virtualization) provides high-level APIs to create and manage Linux and macOS VMs on Apple Silicon and Intel Macs. It exposes VIRTIO devices for networking, storage, sockets, serial I/O, memory ballooning, and related devices. Apple's [GUI Linux sample](https://developer.apple.com/documentation/virtualization/running-gui-linux-in-a-virtual-machine-on-a-mac) installs and runs a GUI Linux VM and displays its graphical content in a `VZVirtualMachineView`; the framework also supports keyboard and pointing-device configuration.

**Verified.** A macOS app using the framework needs the Virtualization entitlement (`com.apple.security.virtualization`). The GUI sample persists VM state in a VM bundle containing a disk image. The framework supports Linux guests on both Apple Silicon and Intel; Apple separately documents Intel-binary translation for Linux VMs on Apple Silicon in supported macOS versions.

**Project inference.** Virtualization.framework gives the cleanest long-term full-guest boundary and makes the desktop a real VM rather than a container process tree. It does not itself provide an OCI image registry, Dockerfile builder, container volume lifecycle, CDP broker, secure remote-display gateway, agent policy, or skill recorder. Those would be OpenAgentFleet services or an additional guest runtime. A Swift helper or native Tauri module would own VM lifecycle while Go remains the controller.

**Best use.** Keep this as a Phase 3 backend for high-risk workloads, VM snapshots/restore, and a future “strong isolation” mode. Do not make the first release depend on a custom VM manager.

### OrbStack

**Verified.** OrbStack's official documentation describes a native macOS app that runs Docker containers and Linux machines, with CLI integration, file/image/volume access, VPN and SSH support, Rosetta x86 emulation, and isolated sandboxes. See [OrbStack documentation](https://docs.orbstack.dev/) and the [official project page](https://orbstack.dev/).

**Project inference.** OrbStack is a useful optional Docker-compatible runtime for users who already have it. It may offer a lighter desktop experience, but OpenAgentFleet should not make its vendor-specific behavior a security or reproducibility requirement. Detect it through the Docker-compatible endpoint and run the same backend acceptance tests. Keep a separate provider ID so diagnostics can report the actual runtime instead of falsely labeling it Docker Desktop.

### LXD and Incus

**Verified.** Canonical's [LXD installation documentation](https://documentation.ubuntu.com/lxd/latest/installing/) says the LXD daemon is installed through Linux mechanisms such as Snap. For macOS it documents Homebrew/native builds of the `lxc` client, explicitly noting that those builds are for the client only, not the LXD daemon.

**Verified.** Incus makes the same distinction: its [installation documentation](https://linuxcontainers.org/incus/docs/main/installing/) says the daemon works only on Linux while the client is available on macOS. It documents Colima as a way to run Incus locally (`colima start --runtime incus`), and the [Incus introduction](https://linuxcontainers.org/incus/) describes macOS as a client connecting to an Incus server on Linux.

**Project inference.** LXD is a good fit for a remote Linux worker that OpenAgentFleet reaches over Tailscale, especially when we want system containers, VMs, snapshots, and a server already running on Linux. It is not a native macOS-first Agent Computer backend. Incus through Colima is worth a later experiment, but it adds another VM/runtime layer and does not remove the need to validate the GUI stack.

## Decision matrix

The matrix below is a project assessment. “Strong” means the required primitive is documented and aligns with the design. “Conditional” means the primitive exists or is plausible but the exact desktop workload needs a prototype. “Custom” means OpenAgentFleet would own a substantial layer. “Remote” means it is not a native local macOS daemon.

| Requirement | Docker Desktop / Docker API | Colima / Lima | Apple `container` | Virtualization.framework | OrbStack | LXD / Incus |
| --- | --- | --- | --- | --- | --- | --- |
| OCI image/build lifecycle | Strong | Strong via Docker/containerd | Strong, OCI + BuildKit documented | Custom | Strong via Docker-compatible runtime | Strong on Linux; remote on Mac |
| Xfce + Chromium + virtual display | Strong for current image; project already runs it | Conditional; same image, VM-specific QA | Conditional; OCI primitives exist, GUI not officially demonstrated | Strong capability, custom guest image | Conditional/likely via Docker image; must test | Conditional on Linux; remote/local Incus path needs QA |
| Persistent Chrome profile | Strong named volume | Strong through Docker volume or VM storage | Strong volume/machine primitives; exact profile QA pending | Strong disk image/guest filesystem | Strong volumes | Strong storage/snapshots on server |
| CDP / Playwright | Strong with published/forwarded endpoint | Strong with forwarded endpoint | Conditional; port/socket wiring needs test | Strong inside guest; controller bridge is custom | Strong through Docker networking | Strong inside guest; remote tunnel is custom |
| Human desktop takeover | Strong with server-gated frame/action control | Strong if Docker path is identical | Conditional; controlled input path not documented as a desktop recipe | Strong display/input APIs, but OpenAgentFleet must build the bridge | Conditional; same controlled image likely | Conditional; remote Linux display path required |
| Loopback + Tailscale remote access | Strong; bind host ports to loopback, proxy botd | Strong; validate port forwarder | Conditional; dedicated IP/port model needs integration | Custom network/port forwarding | Strong/conditional; test provider-specific networking | Remote-first; Tailscale to Linux server |
| Host filesystem boundary | Linux VM boundary; bind mounts remain powerful | Linux VM boundary; explicit mounts required | Per-container lightweight VM; narrow mounts possible | Full VM boundary; sharing is explicit | Vendor-managed isolated sandboxes | System containers share Linux kernel; VMs stronger |
| Apple Silicon | Strong; use arm64 image, Rosetta where needed | Strong; `vz`/Rosetta options | Apple Silicon only; macOS 26 | Strong; Apple Silicon and Intel | Vendor-supported x86 emulation | Depends on Linux server/Colima; Incus VM support on Colima has hardware constraints |
| Intel Mac | Supported by Docker Desktop | Supported by Colima/Lima | Not supported | Supported | Must be checked at install time | Remote Linux works |
| Open-source distribution | Engine/API open; Docker Desktop distribution has terms | Strong; Lima/Colima are open source | Strong; Apple repositories are Apache-2.0, but macOS 26-only | Apple framework is system API; helper is ours | Optional external dependency | Strong server/client components, but daemon is Linux |
| Operational maturity for OpenAgentFleet | Best now | Best OSS-first option | Experimental | Long-term/custom | Optional | Remote/later |

## Runtime-neutral contract

The controller should depend on a normalized backend contract, not on Docker command strings. The exact Go names can change, but the contract needs these operations:

```text
Probe() -> BackendInfo, Capabilities
Ensure(spec) -> Instance
Start(instance)
Stop(instance, graceful)
Delete(instance, preservePersistentData)
Exec(instance, argv[], env, cwd, stdinPolicy) -> process stream
BrowserEndpoint(instance) -> CDP endpoint + health
DesktopEndpoint(instance) -> authenticated display endpoint + input channel
Transfer(instance, direction, path, stream)
Snapshot(instance) -> optional snapshot reference
Restore(snapshot) -> instance
Logs(instance, follow)
```

`spec` should describe the desired state rather than a backend command:

- image or guest template and target architecture;
- CPU, memory, disk, and lifecycle policy;
- explicit workspace mounts with read-only/read-write mode;
- private profile volume and cache policy;
- allowed egress and published services;
- CDP, desktop, and takeover requirements;
- secret-handoff capability;
- reset, export, and retention policy.

Every backend returns a capability report. A UI button must be disabled when the backend cannot safely provide that surface. In particular, “browser ready” must not imply “desktop input ready”, and “desktop visible” must not imply “human takeover authorized”.

### Storage policy

Use three distinct classes of state:

1. **Runtime-private state:** named volume or VM disk for Chromium profile, Xfce state, caches, and guest services. It must not be a bind mount of the user's normal Chrome profile.
2. **Project workspace:** an explicit user-approved host directory, mounted only at the requested path and preferably read-only until the run needs writes.
3. **Ephemeral state:** tmpfs or disposable container layer for screenshots, transient downloads, and test artifacts that should not survive a reset.

The controller must label all resources with the OpenAgentFleet instance ID and backend ID, so cleanup never relies on broad name/glob deletion. Export/import must be a first-class operation before a backend migration.

### Remote access policy

Tailscale is the transport layer, not the container runtime. [Tailscale Serve](https://tailscale.com/docs/reference/examples/serve) can expose a local service privately to the tailnet, while [Tailscale access control](https://tailscale.com/docs/features/access-control) defaults connections to deny unless a policy permits them. The recommended topology is:

```text
iPhone / Android / remote Mac
          |
       Tailscale
          |
Mac: botd bound to loopback
          |
authenticated botd proxy
          |
token-authenticated runtime service / internal CDP / container runtime
```

Do not expose a VNC server, CDP port, Docker socket, or runtime API directly to the tailnet. Serve the controller, authenticate the user there, and proxy only the requested frame/input/browser operation. SSE behavior through the final Tailscale deployment must be an integration test, not an assumption.

## Migration phases

### Phase 0 — stabilize the current Docker path

- Finish the backend-neutral status/capability model around the existing Docker implementation.
- Keep the desktop image `linux/arm64` first; add `linux/amd64` only with an explicit emulation status.
- Move the Chrome profile from a host bind mount to a runtime-private named volume where possible.
- Keep CDP inside the container; publish only the token-authenticated controlled-view port to loopback and keep botd as the only authenticated controller.
- Add acceptance tests for restart, reset, sleep/wake, profile persistence, server-gated desktop input, CDP attachment, and concurrent human/agent control.
- Treat Chromium `--no-sandbox` as a visible security warning and investigate a sandbox-preserving launch before shipping high-risk tasks.

### Phase 1 — open-source macOS distribution

- Detect Docker Desktop, Colima, and OrbStack through the Docker-compatible endpoint.
- Persist the user's runtime choice in `computer.runtime`; resolve the corresponding Docker context inside `botd` and never mutate the global Docker context from the app.
- Document Colima as the open-source reference runtime; do not auto-install a runtime or alter Docker contexts without user approval.
- Use the same OCI image and backend acceptance suite for Docker Desktop and Colima.
- Store a runtime fingerprint in diagnostics: provider, VM type, architecture, kernel, image digest, and mount policy.

### Phase 2 — Apple `container` experimental backend

- Gate on Apple Silicon and macOS 26.
- Build the same OCI image with `container build` and test both `container run` and persistent `container machine`.
- Validate Xfce/Chromium, `shm-size`, CDP, controlled desktop frames/actions, volume persistence, image deletion, and loopback/Tailscale proxying.
- Use narrow mounts and `home-mount=none` unless a specific host share is approved.
- Keep the provider opt-in until two upgrade cycles and a full reset/export/restore test pass.

### Phase 3 — full VM isolation

- Prototype a Swift helper around Virtualization.framework.
- Define a stable guest-agent protocol for process execution, file transfer, desktop frame/input, CDP discovery, and health.
- Add VM disk snapshots and a “strong isolation” run mode.
- Keep OCI image import as an explicit build/packing step; do not pretend the VM backend is Docker-compatible internally.

### Phase 4 — remote Linux and specialist runtimes

- Add an Incus/LXD remote provider over an authenticated connection for a Linux host reachable through Tailscale.
- Consider Incus through Colima only if system-container/VM semantics provide a feature that Docker/Apple Container cannot.
- Keep OrbStack as an optional detected provider, never as a required OpenAgentFleet dependency.

## Final recommendation

For macOS first, the answer is not “Docker forever” and not “Apple Container immediately”. The correct boundary is:

- **Now:** Docker API contract; Docker Desktop works, Colima is the open-source reference runtime, and runtime selection is exposed in Settings/diagnostics.
- **Next:** Apple `container` as an experimental per-container-VM backend on Apple Silicon/macOS 26.
- **Later:** Virtualization.framework for a fully controlled, high-isolation guest VM.
- **Remote:** LXD/Incus on Linux through Tailscale, not a pretend native macOS daemon.
- **Optional:** OrbStack when users already have it and its provider passes the same acceptance suite.

This keeps the Grok-like computer experience—the visible desktop, browser control, and human takeover—stable while the underlying macOS runtime can evolve without rewriting the controller, UI, or agent skills.
