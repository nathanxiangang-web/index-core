// Package rclone implements the first real Collector adapter behind the Collector
// Adapter boundary (ADR-001, doc E Sec 7). It shells out to an external rclone
// process (RC operations/list or CLI lsjson --recursive); it is never linked into
// the Kernel.
//
// Safety posture (accepted Gate 2 boundary): rclone RC/CLI exposes no structured
// skipped-set, so skipped_scopes is always UNKNOWN (absent), never confirmed-empty.
// A successful scan therefore cannot satisfy the frozen completeness gate C-9 and
// this adapter MUST NOT claim destructive-safe COMPLETE. Failure visibility is
// WEAK by default; a caller may qualify a specific mode/backend as STRONG only
// with explicit evidence that errors are typed and propagated (doc E E3, B7).
// Per-object rclone IDs are IdentityEvidence only, never the final Snapshot
// revision token; the final IO3 identity stays Kernel-owned after evaluation.
package rclone

import (
	"context"
	"encoding/json"

	"os/exec"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/collector/adapter"
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

// NormalizedEntry is the neutral adapter output (alias).
type NormalizedEntry = adapter.NormalizedEntry

// RawScan is the neutral adapter scan output (alias).
type RawScan = adapter.RawScan

// Normalize converts rclone lsjson output into normalized evidence, using the
// safe default failure visibility (WEAK). See NormalizeWithAssurance to qualify
// a specific mode/backend.
func Normalize(out []byte, runErr error) RawScan {
	return NormalizeWithAssurance(out, runErr, domain.WeakFailureVisibility)
}

// NormalizeWithAssurance is Normalize with an explicit, mode/backend-qualified
// failure-visibility class. STRONG MUST only be passed when the specific mode
// actually propagates typed errors (B7, doc E E3); it is not a blanket property.
func NormalizeWithAssurance(out []byte, runErr error, assurance domain.FailureVisibility) RawScan {
	if assurance == "" {
		assurance = domain.UnknownFailureVisibility
	}
	scan := RawScan{
		TraversalStatus: domain.TraversalSuccess,
		// No structured skipped-set: absence of evidence is UNKNOWN, not
		// confirmed-empty (doc E Sec 2.3.1).
		SkippedScopes:             nil,
		SkippedKnownEmpty:         false,
		Freshness:                 domain.FreshDirect,
		Assurance:                 assurance,
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
//
// Remote == "" selects the implicit local backend and subPath is used as a local
// filesystem path, which makes the adapter testable across the process boundary
// against a deterministic local directory (B7).
type Adapter struct {
	Binary string
	Remote string
	// ConfigPath, when set, is passed to rclone as --config <path> so a custom
	// rclone configuration (including AList/WebDAV remotes) is actually used.
	// It is never logged.
	ConfigPath string
	// Assurance is the mode/backend-qualified failure visibility. Empty defaults
	// to WEAK (safe). STRONG requires explicit evidence for the mode used.
	Assurance domain.FailureVisibility
	Timeout   time.Duration
}

// Scan runs `rclone lsjson --recursive <target>` and normalizes output.
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
	target := subPath
	if a.Remote != "" {
		target = a.Remote + ":" + subPath
	}
	args := []string{"lsjson", "--recursive"}
	if a.ConfigPath != "" {
		args = append(args, "--config", a.ConfigPath)
	}
	args = append(args, target)
	cmd := exec.CommandContext(ctx, bin, args...)
	out, err := cmd.Output()

	assurance := a.Assurance
	if assurance == "" {
		assurance = domain.WeakFailureVisibility
	}
	if err != nil {
		return NormalizeWithAssurance(nil, err, assurance), nil
	}
	return NormalizeWithAssurance(out, nil, assurance), nil
}
