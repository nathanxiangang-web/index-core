package scan

import (
	"context"
	"errors"

	"github.com/nathanxiangang-web/index-core/internal/domain"
	"github.com/nathanxiangang-web/index-core/internal/incremental/state"
	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
)

// ClaimedScanner adapts the accepted single-observation fence for runtime serial
// mode/fallback. It does not change the ordinary/manual ScanScope contract.
type ClaimedScanner struct{ service *Service }

func NewClaimedScanner(s *Service) *ClaimedScanner { return &ClaimedScanner{service: s} }
func (s *ClaimedScanner) ScanScope(ctx context.Context, root, scope string, max int) (Result, error) {
	if s == nil || s.service == nil {
		return Result{}, errors.New("missing claimed scanner")
	}
	return s.service.ScanScope(ctx, root, scope, max)
}
func (s *ClaimedScanner) ScanClaimedScope(ctx context.Context, w state.DirtyScopeWork, max int) (Result, error) {
	if s == nil || s.service == nil {
		return Result{}, errors.New("missing claimed scanner")
	}
	p, e := NewFetchPrototype(s.service, 1)
	if e != nil {
		return Result{}, e
	}
	f, e := p.FetchScope(ctx, w, max)
	if e != nil {
		return Result{}, e
	}
	defer p.DiscardFetchedScope(f)
	out, e := p.CommitFetchedScope(ctx, f)
	if errors.Is(e, postgres.ErrScopedFetchStale) || (e == nil && out.Outcome.Status != domain.AdmissionApplied && out.Outcome.Status != domain.AdmissionNoop) {
		return Result{}, NewScopeError(ScopeFailureTransientProvider, postgres.ErrScopedFetchStale)
	}
	return out, e
}
