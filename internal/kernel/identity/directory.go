package identity

import (
	"crypto/sha256"
	"encoding/hex"
	"github.com/nathanxiangang-web/index-core/internal/domain"
	"strconv"
)

// DirectoryDigest adds decision-relevant coverage to IO3. Generation and clocks
// are authorization fences, not semantic identity, and are deliberately omitted.
func DirectoryDigest(base domain.SnapshotIdentity, coverage domain.DirectoryCoverage) domain.SnapshotIdentity {
	h := sha256.New()
	writeTag(h, "directory-coverage-v1")
	writeTag(h, base.Value)
	writeTag(h, coverage.Path)
	writeTag(h, coverage.AdapterDigest)
	writeTag(h, strconv.Itoa(coverage.EntryCount))
	base.Version = "v3-directory-coverage"
	base.Value = hex.EncodeToString(h.Sum(nil))
	return base
}
