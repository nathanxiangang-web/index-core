package postgres

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nathanxiangang-web/index-core/internal/health"
)

// Readiness implements health.Probe over a pool WITHOUT exposing the pool or any
// generic SQL/exec capability to consumers (Gate 3 G3-R2).
type Readiness struct {
	pool *pgxpool.Pool
}

// NewReadiness wraps a pool as a read-only readiness probe.
func NewReadiness(pool *pgxpool.Pool) *Readiness { return &Readiness{pool: pool} }

var _ health.Probe = (*Readiness)(nil)

// Ping reports database reachability.
func (r *Readiness) Ping(ctx context.Context) error { return r.pool.Ping(ctx) }

// SchemaStatus reports schema compatibility as a read-only value.
func (r *Readiness) SchemaStatus(ctx context.Context) (health.SchemaInfo, error) {
	s, err := SchemaStatus(ctx, r.pool)
	if err != nil {
		return health.SchemaInfo{}, err
	}
	return health.SchemaInfo{Applied: s.Applied, Required: s.Required, Missing: s.Missing, Unexpected: s.Unexpected}, nil
}
