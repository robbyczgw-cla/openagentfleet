# ADR 0001: mobile alpha is a paired, read-only-capable private client

- **Date:** 2026-08-12
- **Status:** accepted

## Context

OpenAgentFleet runs agents, harnesses, browser sessions, and the Agent Computer
on a user's Mac. iOS and Android are companion clients over a private Tailnet,
not alternative execution hosts. The first prototype used one optional,
daemon-wide bearer token on the full local API. That is unsuitable for a
mobile alpha: it cannot revoke a single lost phone and would expose local-only
administration if Tailscale Serve were pointed at that API.

## Decision

The remote alpha ships only after all of the following exist:

1. A distinct, loopback-only mobile listener (`127.0.0.1:4318`) with an
   explicit `/api/v1/**` allowlist. Tailscale Serve targets this listener,
   never the local administrative API on `4317`.
2. A QR/direct-transfer pairing grant that is random, short-lived, single-use,
   hash-only at rest, and exchanged for an opaque **per-device** bearer
   credential. A bearer credential is labeled exactly that; it is not presented
   as DPoP or hardware-key-bound authentication.
3. A durable device record, per-device credential expiry, local device list,
   and local revocation. Revocation invalidates credentials and closes active
   mobile streams within a bounded interval.
4. A mobile-safe DTO and a cursor-safe durable event stream. Mobile never
   receives workdirs, shell commands, Docker identifiers, native harness
   sessions, OAuth state, preferences, plugin configuration, or secret-handoff
   metadata.
5. No mobile Agent Computer input. The alpha can render authenticated frames;
   computer control is withheld until the server has device-bound control
   leases, epochs, action IDs, and stale-frame protection.

The alpha can support scoped chat and read-only Computer View. Every allowed
mutation has an idempotency contract before it is exposed remotely.

## Non-goals for this decision

- DPoP, refresh-token rotation, Secure Enclave/Android Keystore signing, and
  Tailscale identity-header binding.
- Remote password, OTP, CAPTCHA, payment, desktop, Docker, CDP, VNC/noVNC, or
  arbitrary shell access.
- Public Internet access, Tailscale Funnel, a cloud relay, push delivery, or
  waking a sleeping Mac.
- A generic proxy of the macOS application's API.

## Consequences

The first app build is useful but deliberately narrower than a full remote
desktop. It gives the project a correct migration path: device records include
an auth-version and scope/profile seam, so later DPoP and control leases can be
added by re-pairing rather than silently changing a credential's meaning.

The mobile UI must be explicit about its state. Before pairing is implemented,
it calls itself a development prototype; before leases are implemented, it
shows the Agent Computer as read-only.

## Evidence informing the decision

- The current `4317` handler contains local-only harness OAuth, Teach a Task,
  plugin/skill management, native Grok actions, secret-handoff metadata, and
  computer actions alongside normal chat APIs.
- The initial mobile prototype and a T3 Code comparison showed that a
  long-lived shared token and manual URL paste are the largest safety and UX
  gaps for a private mobile companion.
- Independent architecture reviews by Grok, DeepSeek V4 via Pi, and Fable high
  via Claude reached the same release boundary: listener split, one-time pairing
  exchange, per-device revoke, safe DTOs, and robust SSE must precede alpha;
  device-bound cryptography and remote input should follow, not be imitated.

## Related

- [Mobile remote protocol](../mobile-remote.md)
- [Remote Mac architecture](../remote-mac-architecture.md)
- [Third-party notices](../../NOTICE.md)
