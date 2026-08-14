# Agent Computer FAQ

This page covers the settings most users do not need to think about on the
first run. The default is intentionally modest and ready to use.

## What is the standard configuration?

The default Agent Computer is:

- Ubuntu 24.04;
- 4 CPU;
- 4 GiB RAM;
- 25 GiB disk; and
- 1 GiB guest swap.

The computer starts lazily when Computer View or an approved browser/desktop
task needs it. You do not need to choose an image or tune resources before
your first conversation.

## Which Linux images can I choose?

The supported choices are Ubuntu 24.04 (the default), Ubuntu 26.04 and Debian
13. The image is selected in the advanced Agent Computer settings and is built
into a separate image tag, so changing the choice does not silently reuse the
previous distribution image.

## Can I change CPU, RAM, disk or swap?

Yes. Each value is optional and can be changed independently in Settings:

| Setting | Allowed value |
| --- | ---: |
| CPU | 1–16 |
| RAM | 2–64 GiB |
| Disk | 10–500 GiB |
| Guest swap | 0–16 GiB |

Changes apply the next time the Agent Computer starts. A running computer is
not resized underneath an active session.

## Will a smaller disk setting shrink my existing Colima disk?

No. Colima disks are grow-only here. If the dedicated profile already has a
larger disk, OpenAgentFleet preserves it and reports that fact; it does not
delete or shrink the profile disk. For example, a profile with 100 GiB stays
at 100 GiB when the setting is changed to 25 GiB. A new profile can start with
the requested disk size, and a smaller-than-existing request is safe.

## What happens when the Mac does not have enough free space?

Before starting Colima, OpenAgentFleet checks free space on the filesystem that
holds Colima and on the Agent Computer workspace/profile location. The check
includes the requested VM disk and guest swap, image-layer headroom, and
workspace/profile headroom. If the budget is not available, provisioning is
blocked before a VM or container is created. The UI shows a specific free-space
error and a retry path instead of reporting a vague Docker or Chromium error.

Freeing space and retrying is enough; the preflight does not delete unrelated
files or automatically resize other runtimes.

## Why do Docker Desktop, OrbStack and Colima show different resource behavior?

They provide different resource boundaries even though OpenAgentFleet talks to
each through Docker-compatible commands:

| Runtime | What OpenAgentFleet controls | What the runtime controls |
| --- | --- | --- |
| Colima | VM CPU, VM RAM, VM disk and guest swap | The underlying macOS/VM lifecycle |
| Docker Desktop | Container CPU, RAM and swap limits | VM CPU/RAM/storage and its disk image |
| OrbStack | Container CPU, RAM and swap limits | VM/machine CPU/RAM/storage and its disk management |

For Docker Desktop or OrbStack, the Agent Computer's disk setting is not a
request to resize the runtime's VM disk. If the host runtime has too little
space or memory, adjust that runtime in its own settings as well. The
controller still applies the per-container limits so a selected Agent Computer
cannot quietly consume unlimited CPU or RAM.

## Is swap extra performance?

No. Swap is only a small emergency buffer for short memory spikes. The default
1 GiB helps avoid an abrupt failure when the guest briefly exceeds its RAM, but
it is much slower than RAM and should not be used to make a workload larger.
Set guest swap to `0` to disable the explicit app-owned buffer, or increase RAM
for a workload that regularly needs more memory.

On Colima, this is guest swap; macOS host swap is not the same resource. On
Docker Desktop and OrbStack, the value participates in the container's memory
and swap limit while the runtime's own VM policy remains separate.

## Do settings change the global Docker context?

No. The selected runtime and its resource settings are applied to the
controller instance. Colima uses the dedicated `openagentfleet` profile, and
OpenAgentFleet does not silently switch the global Docker context.

For the full backend contract and advanced isolation decisions, see
[macOS Agent Computer Backends](macos-agent-computer-backends.md).
