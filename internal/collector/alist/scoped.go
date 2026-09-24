package alist

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/collector/adapter"
	"github.com/nathanxiangang-web/index-core/internal/domain"
)

// MaxScopedEntries is the P0 hard cap on a single scoped observation. Callers
// must never be able to widen it: the adapter fails closed above this bound so a
// runaway per_page (e.g. integer overflow from maxEntries+1) can never reach the
// provider.
const MaxScopedEntries = 10000

// ScanScope is the P0 Targeted Scoped Refresh observation (Issue #62).
//
// It observes ONLY the direct children of scope using EXACTLY ONE forced
// (`refresh=true`) /api/fs/list request. It never paginates, never recurses, and
// fails closed (returns an error, no Snapshot) unless the provider-declared
// `total` equals the returned content AND fits within maxEntries.
//
// The returned evidence is deliberately additive-safe:
//
//	TraversalStatus           = PARTIAL
//	Freshness                 = FRESH_REFRESHED
//	Assurance                 = WEAK_FAILURE_VISIBILITY
//	ProviderIdentityAssurance = UNVERIFIED
//	SkippedScopes             = UNKNOWN (nil, never confirmed-empty)
//
// An empty directory is a legal PARTIAL zero-entry observation; absence inside a
// scoped response MUST NOT be interpreted as removal by the Kernel.
//
// This is a prototype-only path: it does not modify the generic Collector
// interface or the existing Adapter.Scan() behavior.
func (a Adapter) ScanScope(ctx context.Context, scope string, maxEntries int) (adapter.RawScan, error) {
	if a.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, a.Timeout)
		defer cancel()
	}
	if maxEntries < 0 || maxEntries > MaxScopedEntries {
		return adapter.RawScan{}, scopedErrorf(ScopedConfigInvalid,
			"alist scoped list: max_entries must be within [0, %d], got %d", MaxScopedEntries, maxEntries)
	}
	// A persisted base URL that cannot form a valid provider request is a
	// permanent adapter configuration defect, never a retryable provider
	// failure: fail before any provider I/O.
	if err := validateScopedBaseURL(a.BaseURL); err != nil {
		return adapter.RawScan{}, scopedErrorf(ScopedConfigInvalid,
			"alist scoped refresh base URL is invalid: %v", err)
	}
	// maxEntries <= MaxScopedEntries, so maxEntries+1 cannot overflow.
	perPage := maxEntries + 1

	token := a.Token
	if token == "" && a.Username != "" {
		t, err := a.loginScoped(ctx)
		if err != nil {
			return adapter.RawScan{}, err
		}
		token = t
	}

	dir := normalizePath(scope)

	// Exactly one request. per_page = maxEntries+1 lets us detect overflow
	// (total > maxEntries) without ever requesting a second page, and guarantees
	// that a non-overflowing response contains the whole directory in one
	// coherent generation.
	items, total, err := a.listPageRefresh(ctx, token, dir, perPage)
	if err != nil {
		return adapter.RawScan{}, err
	}
	if total > maxEntries {
		return adapter.RawScan{}, scopedErrorf(ScopedTooLarge,
			"alist scoped list %q exceeds max_entries: total=%d max_entries=%d", dir, total, maxEntries)
	}
	if total != len(items) {
		return adapter.RawScan{}, scopedErrorf(ScopedTransientProvider,
			"alist scoped list %q total/count mismatch: total=%d content=%d", dir, total, len(items))
	}

	entries := make([]adapter.NormalizedEntry, 0, len(items))
	for _, it := range items {
		e := adapter.NormalizedEntry{
			EntryLocalID: joinLocal(dir, it.Name),
			Name:         it.Name,
			ParentRef:    dir,
			IsDir:        it.IsDir,
		}
		size := it.Size
		e.Size = &size
		if t, perr := time.Parse(time.RFC3339, it.Modified); perr == nil {
			utc := t.UTC()
			e.Mtime = &utc
		}
		if alg, sum, ok := pickHash(it.HashInfo); ok {
			e.ContentHash = &sum
			e.HashAlgorithm = &alg
		}
		entries = append(entries, e)
	}

	return adapter.RawScan{
		Entries:                   entries,
		TraversalStatus:           domain.TraversalPartial,
		SkippedScopes:             nil, // UNKNOWN, never confirmed-empty
		SkippedKnownEmpty:         false,
		Freshness:                 domain.FreshRefreshed,
		Assurance:                 domain.WeakFailureVisibility,
		ProviderIdentityAssurance: domain.IdentityUnverified,
	}, nil
}

// listPageRefresh performs the single forced directory listing. It never loops.
func (a Adapter) listPageRefresh(ctx context.Context, token, dir string, perPage int) ([]item, int, error) {
	body, _ := json.Marshal(map[string]any{
		"path":     dir,
		"password": "",
		"page":     1,
		"per_page": perPage,
		"refresh":  true,
	})
	var resp apiResp
	if err := a.post(ctx, "/api/fs/list", token, body, &resp); err != nil {
		// Transport failure / provider-side timeout / malformed body.
		return nil, 0, scopedWrap(ScopedTransientProvider, err)
	}
	if resp.Code != http.StatusOK {
		// Surfaces refresh-permission failures (403 "Refresh without permission")
		// and any other provider/list error as a typed kind.
		return nil, 0, scopedErrorf(scopedKindFromAPICode(resp.Code),
			"alist scoped refresh %q failed: code=%d message=%s", dir, resp.Code, resp.Message)
	}
	var data *scopedListData
	if err := json.Unmarshal(resp.Data, &data); err != nil {
		return nil, 0, scopedWrap(ScopedTransientProvider, err)
	}
	// The provider must explicitly return both content and total. Missing/null
	// fields must never be completed from Go zero values into a fabricated empty
	// directory.
	if data == nil || data.Content == nil || data.Total == nil {
		return nil, 0, scopedErrorf(ScopedTransientProvider,
			"alist scoped refresh %q returned an incomplete payload (content/total missing)", dir)
	}
	if *data.Total < 0 {
		return nil, 0, scopedErrorf(ScopedTransientProvider,
			"alist scoped refresh %q returned a negative total %d", dir, *data.Total)
	}
	// total == len(content) is intentionally enforced by ScanScope AFTER the
	// overflow check: a legitimate total > per_page truncation is an overflow,
	// not a malformed short page.
	return *data.Content, *data.Total, nil
}

// scopedListData is the scoped decoder shape. Field presence is significant:
// content and total must both be provider-declared (an empty array is a legal
// empty directory, but a missing/null field is not).
type scopedListData struct {
	Content *[]item `json:"content"`
	Total   *int    `json:"total"`
}

// validateScopedBaseURL rejects a persisted base URL that can never form a valid
// provider request. Legitimate path-prefixed deployments (e.g.
// "http://host/alist") remain valid.
func validateScopedBaseURL(raw string) error {
	s := strings.TrimSpace(raw)
	if s == "" {
		return fmt.Errorf("base URL is empty")
	}
	u, err := url.Parse(s)
	if err != nil {
		return fmt.Errorf("base URL is not a valid URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("base URL scheme must be http or https, got %q", u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("base URL must include a host")
	}
	return nil
}

// loginScoped obtains a login token for the scoped-refresh path with the same
// typed provider classification as the list call. It deliberately does not use
// the generic Adapter.login, whose errors are untyped.
func (a Adapter) loginScoped(ctx context.Context) (string, error) {
	body, _ := json.Marshal(map[string]string{"username": a.Username, "password": a.Password})
	var resp apiResp
	if err := a.post(ctx, "/api/auth/login", "", body, &resp); err != nil {
		return "", scopedWrap(ScopedTransientProvider, err)
	}
	if resp.Code != http.StatusOK {
		return "", scopedErrorf(scopedKindFromAPICode(resp.Code),
			"alist scoped login failed: code=%d message=%s", resp.Code, resp.Message)
	}
	var data struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(resp.Data, &data); err != nil {
		return "", scopedWrap(ScopedTransientProvider, err)
	}
	if data.Token == "" {
		return "", scopedErrorf(ScopedTransientProvider, "alist scoped login returned no token")
	}
	return data.Token, nil
}
