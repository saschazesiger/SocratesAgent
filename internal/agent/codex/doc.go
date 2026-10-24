// Package codex is the Codex adapter: it drives one `codex app-server` per
// chat over its stdio JSON-RPC protocol and maps threads, turns and items onto
// the chat event model of package agent (REBUILD.md §3.2, AGENT-HOST.md §5).
//
// The app-server is a child of the chat's agent host, which is what makes it
// durable: no --listen socket and never the managed daemon. Socrates always
// allows everything (approvalPolicy "never", sandbox "danger-full-access"),
// and answers every server request so that nothing waits forever.
//
// It is registered under the harness name "codex" on Unix; the agent host is
// Unix-only.
package codex
