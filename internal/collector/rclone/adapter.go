// Package rclone implements the first real Collector adapter behind the Collector
// Adapter boundary (ADR-001, doc E Sec 7). It shells out to an external rclone
// process (RC operations/list or CLI lsjson --recursive); it is never linked into
// the Kernel.
//
// Safety posture (accepted Gate 2 boundary): rclone RC/CLI exposes no structured
// skipped-set, so skipped_scopes is always UNKNOWN (absent), never confirmed-empty.
// A successful scan therefore cannot satisfy the frozen completeness gate C-9 and
// this adapter MUST NOT claim destructive-safe COMPLETE. Per-object rclone IDs are
// IdentityEvidence only, never the final Snapshot revision token; the final IO3
// identity stays Kernel-owned after evaluation.
package rclone

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/domain"
)

// Entry is one object as emitted by `rclone lsjson --recursive`.
type Entry struct {
	Path     string            `json:"Path"`
	Name     string            `json:"Name"`
	Size     int64             `json:"Size"`
	MimeType string            `json:"MimeType"`
	ModTime  string            `json:"ModTime"`
	IsDir    bool              `json:"IsDir"`
	ID       string            `json:"ID"`
	Hashes   map[string]string `json:"Hashes"`
}

// NormalizedEntry is a provider-neutral SnapshotEntry candidate. ProviderObjectID
// is optional evidence only; it is never the Snapshot revision token.
type NormalizedEntry struct {
	EntryLocalID          string
	Name                  string
	ParentRef             string
	IsDir                 bool
	Size                  *int64
	Mtime                 *time.Time
	ContentHash           *string
	HashAlgorithm         *string
	ProviderObjectID      *string
	ProviderObjectIDScope *string
	ContentType           *string
}

// RawScan is the normalized Collector evidence for one scan.
type RawScan struct {
	Entries                   []NormalizedEntry
	TraversalStatus           domain.TraversalStatus
	ErrorSummary              []byte
	SkippedScopes             []byte
	SkippedKnownEmpty         bool
	Freshness                 domain.FreshnessEvidence
	Assurance                 domain.FailureVisibility
	ProviderIdentityAssurance domain.ProviderIdentityAssurance
}

// Normalize converts rclone lsjson output into normalized, provider-neutral
// evidence. runErr is the process error (nil on success). This is pure and
// unit-testable without an rclone installation.
func Normalize(out []byte, runErr error) RawScan {
	scan := RawScan{
		TraversalStatus: domain.TraversalSuccess,
		// No structured skipped-set: absence of evidence is UNKNOWN, not
		// confirmed-empty (doc E Sec 2.3.1).
		SkippedScopes:             nil,
		SkippedKnownEmpty:         false,
		Freshness:                 domain.FreshDirect,
		Assurance:                 domain.StrongFailureVisibility,
		ProviderIdentityAssurance: domain.IdentityUnverified,
	}
	if runErr != nil {
		scan.TraversalStatus = domain.TraversalFailed
		scan.ErrorSummary = jsonError("error", runErr.Error())
		return scan
	}
	var raw []Entry
	if err := json.Unmarshal(out, &raw); err != nil {
		scan.TraversalStatus = domain.TraversalFailed
		scan.ErrorSummary = jsonError("parse_error", err.Error())
		return scan
	}
	for _, e := range raw {
		scan.Entries = append(scan.Entries, normalizeEntry(e))
	}
	return scan
}

func normalizeEntry(e Entry) NormalizedEntry {
	n := NormalizedEntry{
		EntryLocalID: e.Path,
		Name:         e.Name,
		ParentRef:    path.Dir("/" + strings.TrimPrefix(e.Path, "/")),
		IsDir:        e.IsDir,
	}
	if e.Size > 0 || !e.IsDir {
		size := e.Size
		n.Size = &size
	}
	if t, err := time.Parse(time.RFC3339Nano, e.ModTime); err == nil {
		utc := t.UTC()
		n.Mtime = &utc
	}
	if alg, sum, ok := pickHash(e.Hashes); ok {
		n.ContentHash = &sum
		n.HashAlgorithm = &alg
	}
	if e.ID != "" {
		id := e.ID
		scope := "rclone"
		n.ProviderObjectID = &id
		n.ProviderObjectIDScope = &scope
	}
	if e.MimeType != "" && !e.IsDir {
		m := e.MimeType
		n.ContentType = &m
	}
	return n
}

// pickHash selects a deterministic content hash, preferring SHA-1 then MD5.
func pickHash(hashes map[string]string) (algorithm, sum string, ok bool) {
	if len(hashes) == 0 {
		return "", "", false
	}
	for _, pref := range []string{"SHA-1", "MD5"} {
		if v, exists := hashes[pref]; exists && v != "" {
			return strings.ToLower(pref), v, true
		}
	}
	keys := make([]string, 0, len(hashes))
	for k := range hashes {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	k := keys[0]
	if hashes[k] == "" {
		return "", "", false
	}
	return strings.ToLower(k), hashes[k], true
}

func jsonError(key, msg string) []byte {
	b, _ := json.Marshal(map[string]string{key: msg})
	return b
}

// Adapter runs an external rclone process to produce normalized evidence.
type Adapter struct {
	Binary  string
	Remote  string
	Timeout time.Duration
}

// Scan runs `rclone lsjson --recursive <remote>:<subPath>` and normalizes output.
func (a Adapter) Scan(ctx context.Context, subPath string) (RawScan, error) {
	bin := a.Binary
	if bin == "" {
		bin = "rclone"
	}
	if a.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, a.Timeout)
		defer cancel()
	}
	target := a.Remote + ":" + subPath
	cmd := exec.CommandContext(ctx, bin, "lsjson", "--recursive", target)
	out, err := cmd.Output()
	if err != nil {
		return Normalize(nil, err), nil
	}
	return Normalize(out, nil), nil
}

// MinIOPageSize is a compile-time documentation anchor for the additive-safe role.
const MinIOPageSize = 100

func (s RawScan) String() string {
	return fmt.Sprintf("rclone scan: %d entries, status=%s, skippedKnownEmpty=%v",
		len(s.Entries), s.TraversalStatus, s.SkippedKnownEmpty)
}
