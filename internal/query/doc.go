// Package query implements the read-only Query Contract (Issue #44 P6): root
// and resource reads, hierarchy and explicit path resolution, active/removed
// reads, root status/generation, per-root Journal cursor reads, and
// generation-bound pagination returning STALE_CURSOR. It exposes no canonical
// write path (doc C, W1..W5).
package query