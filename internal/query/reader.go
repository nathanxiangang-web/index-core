package query

import "context"

// Reader is the consumer-facing read-only Query Contract surface. A consumer
// receives only this interface: never *postgres.Store, never pgxpool.Pool, and
// never a mutating method (doc C Sec 0.3 / W1..W3, B5).
type Reader interface {
	ListRoots(ctx context.Context, includeDeprecated, includeDeleted bool) ([]RootView, error)
	RootStatus(ctx context.Context, rootID string) (RootStatus, error)
	GetResource(ctx context.Context, resourceID string, includeRemoved bool) (*ResourceView, error)
	ResolvePath(ctx context.Context, rootID, path string, includeRemoved bool) (PathResolution, error)
	ListActivePage(ctx context.Context, rootID string, cur *Cursor, limit int) (ResourcePage, error)
	ListRemovedPage(ctx context.Context, rootID string, cur *Cursor, limit int) (ResourcePage, error)
	ReadJournal(ctx context.Context, rootID string, afterSeq int64, limit int) ([]JournalEventView, error)
}
