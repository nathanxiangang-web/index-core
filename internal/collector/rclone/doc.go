// Package rclone implements the first real Collector adapter (Issue #44 P8)
// behind the Collector Adapter boundary, using an external rclone process
// (RC operations/list or CLI lsjson --recursive). It normalizes SnapshotEntry
// fields, preserves optional provider object id/hash, declares identity
// assurance honestly, and keeps skipped_scopes UNKNOWN unless positively
// established. It never claims destructive-safe COMPLETE while skip evidence is
// UNKNOWN, and per-object rclone ids are evidence only, never the final Snapshot
// revision token (ADR-001, doc E Sec 7).
package rclone