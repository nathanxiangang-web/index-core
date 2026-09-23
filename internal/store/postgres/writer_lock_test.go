package postgres_test

import (
	"errors"
	"testing"

	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
)

// G3-R5: the single active write daemon is enforced by a DB advisory lock; a
// second owner fails closed, and the lock is reusable after release.
func TestWriterLockIsExclusivePerDatabase(t *testing.T) {
	st, ctx := newStore(t)

	l1, err := st.AcquireWriterLock(ctx)
	if err != nil {
		t.Fatalf("first writer lock: %v", err)
	}
	if _, err := st.AcquireWriterLock(ctx); !errors.Is(err, postgres.ErrWriterLockHeld) {
		t.Fatalf("second writer lock must fail closed, got %v", err)
	}
	l1.Release(ctx)

	l2, err := st.AcquireWriterLock(ctx)
	if err != nil {
		t.Fatalf("lock must be reusable after release: %v", err)
	}
	l2.Release(ctx)
}
