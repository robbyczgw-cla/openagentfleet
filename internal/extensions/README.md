# Optional Extension Lifecycle Core

`extensions` is a side-effect-free domain package for optional plugins and
connectors. It is intentionally **not** an installer, package manager, MCP
launcher, credential store, process runner, or network client.

## Safe defaults

- Installation records a pinned manifest but leaves it disabled.
- Enabling requires `Policy.ExperimentalExtensionsEnabled`.
- Unverified provenance is rejected unless the caller explicitly opts in.
- A required secret is represented only by a `SecretRef` name. Secret values
  cannot be represented by this package.
- Updates are reviewable `UpdatePlan` metadata. Recording an update disables
  the extension again; a later runtime must receive a new explicit enable.
- Uninstall preserves manifest provenance and an audit trail, while a future
  installer independently owns physical deletion.

## Manifest contract

A manifest has an exact SemVer version pin, SHA-256 digest, public HTTPS
origin, publisher, license, capabilities, and optional secret references.
Resolver-free URL validation rejects direct private/loopback IP addresses;
network code must still apply DNS-aware egress policy before fetching.

## Integration boundary

Future storage/UI/API code should persist the complete `Extension` record and
surface `Policy` as explicit Experimental settings. A runtime must check both
`Enabled` and its own capability/approval policy; this package alone grants no
execution authority.
