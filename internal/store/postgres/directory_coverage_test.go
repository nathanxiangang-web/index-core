package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/nathanxiangang-web/index-core/internal/domain"
)

func TestDirectoryCoverageLeavesLegacyProvenanceOpaque(t *testing.T) {
	store := &Store{}
	for _, raw := range []string{`{}`, `[]`, `1`, `"legacy"`, `null`} {
		proof, err := store.directoryCoverage(context.Background(), nil, domain.Snapshot{Provenance: []byte(raw)}, nil, 0)
		if proof != nil || err != nil {
			t.Fatalf("legacy provenance %s: %v %v", raw, proof, err)
		}
	}
	for _, raw := range []string{`{"directory_coverage":null}`, `{"directory_coverage":"invalid"}`} {
		_, err := store.directoryCoverage(context.Background(), nil, domain.Snapshot{Provenance: []byte(raw)}, nil, 0)
		if !errors.Is(err, ErrDirectoryCoverage) {
			t.Fatalf("bad explicit proof was accepted: %s %v", raw, err)
		}
	}
}
