// Package policy implements the optional, fail-closed OpenAgentFleet
// capability broker. It evaluates one concrete action at a time against
// principal-scoped rules and short-lived, action-bound approvals.
//
// A broker is disabled by default, and disabled or unmatched actions are
// denied. An allow decision never expands authority granted by orchestration;
// callers must satisfy both layers before performing work.
//
// Folder matching is lexical. Callers crossing trust boundaries must resolve
// symlinks before constructing both rules and actions.
package policy
