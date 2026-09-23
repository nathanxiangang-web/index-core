package query

import "context"

// Reader is the consumer-facing read-only Query Contract surface. A consumer
// receives only this interface: never *postgres.Store, never pgxpool.Pool, and
// never a mutating method (doc C Sec 0.3 / W1..W3, B5).
type Reader interface {
	// Q2 list roots; Q1 get root.
	ListRoots(ctx context.Context, includeDeprecated, includeDeleted bool) ([]RootView, error)
	GetRoot(ctx context.Context, rootID string, includeDeprecated, includeDeleted bool) (*RootView, error)
	RootStatus(ctx context.Context, rootID string) (RootStatus, error)
	// Q3 get resource; Q4 list resources/hierarchy children.
	GetResource(ctx context.Context, resourceID string, includeRemoved bool) (*ResourceView, error)
	ListResources(ctx context.Context, rootID string, parentResourceID *string, includeRemoved bool, limit int) (ResourcePage, error)
	// Q5 resolve path; Q6/Q7 active/removed pages.
	ResolvePath(ctx context.Context, rootID, path string, includeRemoved bool) (PathResolution, error)
	ListActivePage(ctx context.Context, rootID string, cur *Cursor, limit int) (ResourcePage, error)
	ListRemovedPage(ctx context.Context, rootID string, cur *Cursor, limit int) (ResourcePage, error)
	// Q8 journal; Q9 root status.
	ReadJournal(ctx context.Context, rootID string, afterSeq int64, limit int) ([]JournalEventView, error)
}
