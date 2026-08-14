// Package isolation plans fail-closed, per-session worker sandboxes.
//
// It owns no process execution. A plan is a deterministic description that a
// future runtime may execute only after it has separately checked the product
// execution gate and approvals. This package deliberately does not share the
// long-lived Agent Computer implementation: workers are disposable, narrowly
// mounted jobs while the Agent Computer is a user-visible desktop.
//
// The Docker plan is the initial strong profile. It requires a non-root user,
// explicit approved mounts, a read-only root filesystem, no Linux capabilities,
// no-new-privileges, resource limits, and an offline network. Network
// allowlists are represented but cannot be converted to Docker arguments until
// an external egress enforcer exists; attempting to do so fails closed.
//
// Native-host planning is disabled by default and is intentionally weaker than
// container isolation. Apple Container is a reserved, unsupported profile.
package isolation
