// Package health defines the narrow read-only readiness abstraction shared by the
// runtime and the HTTP transport. It deliberately exposes no generic SQL/exec
// capability (Gate 3 G3-R2).
package health

import "context"

// SchemaInfo is a read-only schema compatibility report.
type SchemaInfo struct {
	Applied    int
	Required   int
	Missing    []string
	Unexpected []string
}

// Compatible reports exact schema compatibility (no missing, no future).
func (s SchemaInfo) Compatible() bool { return len(s.Missing) == 0 && len(s.Unexpected) == 0 }

// Probe is the only database capability the HTTP transport may hold.
type Probe interface {
	Ping(ctx context.Context) error
	SchemaStatus(ctx context.Context) (SchemaInfo, error)
}
