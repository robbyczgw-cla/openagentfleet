# Security policy

OpenAgentFleet is an early macOS-first alpha. It can start provider CLIs,
browser automation, a Linux desktop computer, remote computer workers and
credential handoff, so security reports are welcome and should be private.

## What counts as a vulnerability

Please report a flaw that can cause an attacker or an unapproved agent to:

- escape the Agent Computer or access host files outside an explicit grant;
- bypass an approval, takeover, run-capability, pairing or authentication
  boundary;
- obtain provider credentials, browser passwords, one-time codes or private
  conversation data without the user's explicit action;
- turn a loopback or Tailscale-only endpoint into an unintended public or
  cross-user service;
- execute commands, browser actions, file changes or network requests outside
  the configured capability and permission policy; or
- exploit the installer, update path, packaged sidecars or release artifacts.

Model mistakes, unsafe instructions supplied by a user, provider-side model
behavior, missing feature parity and ordinary application bugs are not usually
security vulnerabilities. If an ordinary bug could become a security issue,
report it privately and explain the escalation path.

## Current security boundary

The controller and provider processes run as the logged-in macOS user. The
provider CLI is not a separate privilege boundary. In particular, the current
alpha does not claim full provider-process isolation.

The isolated Agent Computer is the browser and desktop boundary:

- it runs a non-root Linux desktop in a Docker-compatible VM/runtime;
- it receives only explicitly approved workspace and browser-profile mounts;
- it does not receive the host Docker socket or the host filesystem root;
- Chromium, Xfce, Terminal and Files stay inside that computer;
- browser/desktop actions require the controller's authenticated, bounded
  action contract; and
- remote computers require a separate authenticated worker route and should be
  reachable only over a private network such as Tailscale.

The boundary does not protect against a malicious process that already runs as
the same macOS user, macOS root, a compromised Docker/Colima runtime, a
malicious provider process, or a user who explicitly grants a sensitive host
path or credential to a third party. Inference may also happen on a provider's
service according to that provider's terms.

Password and one-time-code handoff is deliberately narrower than ordinary
typing: the native macOS secure field sends the value once to the approved
focused browser target and does not place it in chat history, React state,
Teach recordings or model context.

## Reporting privately

Do not open a public issue for a suspected vulnerability. Use GitHub's private
security advisory form for this repository:

<https://github.com/robbyczgw-cla/openagentfleet/security/advisories/new>

If GitHub does not expose the form for your account, contact the repository
owner privately through GitHub and include **Security report** in the subject.
Please include:

1. the affected commit, release or build;
2. macOS and runtime details;
3. a minimal reproduction or proof of concept;
4. the security boundary that is bypassed and the likely impact; and
5. logs or screenshots with tokens, passwords, OTPs, cookies and private data
   removed.

We aim to acknowledge a report within three business days, triage it within
seven days and coordinate a fix or mitigation according to severity. Please
allow time for a private fix before public disclosure.

## Release security checklist

Before a public macOS release, maintainers must review dependency licenses and
checksums, sign and notarize the app and sidecars, verify entitlements, run
the fresh-user and Agent Computer acceptance suites, and confirm that no
credentials or disposable runtime databases are present in the artifact.
