# Worker isolation planning core

`internal/isolation` is a non-executing, fail-closed plan builder for one
disposable harness worker session. It is deliberately separate from
`internal/compute`, which owns the persistent, user-visible Agent Computer.

## Contract

The controller supplies a `Policy` and then calls `Planner.Plan(Spec)`. The
result contains deterministic Docker argv plus a label-scoped cleanup plan. It
does not call Docker, start a process, remove a container, change a mount, or
resolve a secret.

For Docker, a successful plan requires all of the following:

- a non-root numeric `uid:gid`;
- a read-only root filesystem;
- `--cap-drop ALL` and `no-new-privileges=true`;
- PID, CPU, and memory limits;
- at least one bounded tmpfs for writable guest state;
- a network-off policy, rendered as `--network none`;
- every mount to resolve beneath a controller-approved root;
- separate controller approval for any read-write mount; and
- opaque secret references only. `Source` and `Reference` are separate, and
  no secret value exists in `Spec`.

The planner refuses home directories, root, Docker sockets/configuration,
system paths, controller state-secret roots, unknown mount modes, duplicate
guest targets, relative paths, and mount paths which resolve through a symlink
to one of those targets. The Docker image is `--pull=never`; worker execution
does not turn the Docker daemon into a registry client implicitly.

## Profiles

| Profile | Current behavior |
| --- | --- |
| `docker` | Produces deterministic secure `docker run` argv. This is the intended default for eligible worker jobs. |
| `native_host` | Disabled by `DefaultPolicy`. If a controller opts in, provides an auditable reason, and selects an existing workdir inside an approved root, it produces a non-executing declaration only. It is a weaker same-host boundary; a future executor must still enforce its declared resource and network requirements. |
| `apple_container` | Reserved and fails with `ErrProfileUnsupported`; it never falls back to Docker or native. |

## Network boundary

`off` is enforceable by Docker and is the default. `allowlist` accepts only
CIDR, protocol, and port rules, never domain names. A Docker run command cannot
enforce those rules by itself, so planning an allowlist returns
`ErrEgressEnforcerRequired` until a future runtime provides a firewall or
authenticated egress proxy. It is intentionally impossible to accidentally
claim that an allowlist is enforced before that exists.

## Cleanup lifecycle

Each Docker plan is named `openagentfleet-worker-<session-id>` and carries two
OpenAgentFleet labels. Its `CleanupPlan` describes label-filtered orphan lookup,
a bounded graceful stop, and forced removal of that exact name. It never
deletes any host workspace or mount source. Future execution wiring must store
the session lease, call cleanup in a `defer`, and reap only containers carrying
the ownership label after a lease/TTL check.

## Deliberate non-goals in this slice

- no Docker/Apple/native execution;
- no generic runtime or shell command runner;
- no automatic network proxy, DNS filtering, or outbound access;
- no secrets, secret resolution, or secret files; and
- no integration into harnesses until the Lead-to-Worker runtime makes the
  controller-owned policy and approval boundary explicit.
