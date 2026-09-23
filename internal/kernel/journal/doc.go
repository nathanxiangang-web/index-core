// Package journal implements the Canonical Change Journal semantics (Issue #44
// P5): per-root event_seq allocation, same-generation intra_generation_seq
// ordering, append-only guarantees, projection catch-up/rebuild/checkpoint, and
// the J6 Journal-repair transaction (append without canonical mutation or
// generation advance; permitted on a DELETED root) per frozen doc D.
package journal