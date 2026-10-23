// Package opencode is the OpenCode adapter: one `opencode serve` per active
// chat, a child of the chat's agent host, driven through OpenCode's classic
// HTTP API (/session/…) and followed on its /event stream (REBUILD.md §3.3,
// research 05 and 06). The event mapping and the content shapes it writes are
// in docs/design/AGENT-HOST.md §5.
//
// Every process shares one OpenCode database under the Socrates data folder,
// and each account has a data home of its own that holds only its
// credentials (Layout), so that a chat can move between accounts by stopping
// its process and starting one for the other account on the same session.
package opencode
