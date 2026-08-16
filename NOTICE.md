# OpenAgentFleet runtime notices

OpenAgentFleet source code is licensed under [Apache-2.0](LICENSE). This file
contains only notices for third-party runtime components that OpenAgentFleet
bundles or can explicitly launch. Product research and interaction ideas are
not product dependencies and are intentionally not listed here.

## OpenCode 1.18.10

The macOS and Linux packages may distribute the unmodified architecture-correct
OpenCode `1.18.10` executable as an optional sidecar. OpenCode is licensed under the MIT
License; the upstream license is maintained at:

- <https://github.com/anomalyco/opencode/blob/v1.18.10/LICENSE>
- <https://opencode.ai>

## uv and uvx

The macOS and Linux packages may include `uv` and `uvx` launchers from Astral for the
optional search connector runtime. They are dual-licensed under Apache-2.0 and
MIT; the upstream project and license texts are:

- <https://github.com/astral-sh/uv>
- <https://github.com/astral-sh/uv/blob/main/LICENSE-APACHE>
- <https://github.com/astral-sh/uv/blob/main/LICENSE-MIT>

## Optional search connectors

Web Search Plus, Hound, and Donsetch are not silently installed or enabled. If
a user explicitly enables one, the controller may resolve its upstream MCP
package at runtime. Their own license and version information remains with the
upstream package and must be reviewed again before a public packaged release.

- <https://www.websearchplus.xyz/>
- <https://github.com/dondai44423/master-fetch>
- <https://github.com/dondai44423/donsetch> (AGPL-3.0-only)

For every signed/notarized release, maintainers must regenerate the exact
dependency and binary inventory for that release and include any additional
required license or NOTICE text here.
