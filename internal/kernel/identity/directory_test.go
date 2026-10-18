package identity

import (
	"github.com/nathanxiangang-web/index-core/internal/domain"
	"testing"
)

func TestDirectoryDigestScopeBoundAndClockIndependent(t *testing.T) {
	base := domain.SnapshotIdentity{Value: "empty", Version: DigestVersion}
	p := domain.DirectoryCoverage{Version: 1, Path: "/a", AdapterDigest: "config", Generation: 3}
	one := DirectoryDigest(base, p)
	p.Generation = 7
	if DirectoryDigest(base, p) != one {
		t.Fatal("generation leaked into IO3")
	}
	p.Path = "/b"
	if DirectoryDigest(base, p) == one {
		t.Fatal("different empty scopes alias")
	}
}
