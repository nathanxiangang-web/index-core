package alist

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/nathanxiangang-web/index-core/internal/sourcebudget"
	"io"
	"net/http"
	"strings"
)

func (a Adapter) verifyScopedIdentity(ctx context.Context, token string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(a.BaseURL, "/")+"/api/me", nil)
	if err != nil {
		return scopedErrorf(ScopedConfigInvalid, "invalid identity endpoint")
	}
	req.Header.Set("Authorization", token)
	client := a.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		if errors.Is(err, sourcebudget.ErrDeferred) {
			return scopedWrap(ScopedThrottled, sourcebudget.ErrDeferred)
		}
		if ctx.Err() != nil {
			return scopedWrap(ScopedTransientProvider, ctx.Err())
		}
		return scopedErrorf(ScopedTransientProvider, "source identity transport failed")
	}
	defer resp.Body.Close()
	var envelope apiResp
	if resp.StatusCode != 200 {
		return scopedErrorf(scopedKindFromAPICode(resp.StatusCode), "source identity HTTP status not successful")
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&envelope) != nil {
		return scopedErrorf(ScopedTransientProvider, "source identity response invalid")
	}
	var identity struct {
		BasePath string `json:"base_path"`
	}
	if envelope.Code != 200 {
		return scopedErrorf(scopedKindFromAPICode(envelope.Code), "source identity API status not successful")
	}
	if json.Unmarshal(envelope.Data, &identity) != nil || identity.BasePath != a.ExpectedBasePath {
		return scopedErrorf(ScopedAuthOrPermission, "source identity namespace mismatch")
	}
	return nil
}

// Unlike the legacy generic helper, scoped presence must validate HTTP status
// as well as the JSON API envelope before it can establish source coverage.
func (a Adapter) postScoped(ctx context.Context, endpoint, token string, body []byte, out *apiResp) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(a.BaseURL, "/")+endpoint, bytes.NewReader(body))
	if err != nil {
		return scopedErrorf(ScopedConfigInvalid, "invalid scoped endpoint")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", token)
	client := a.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		if errors.Is(err, sourcebudget.ErrDeferred) {
			return scopedWrap(ScopedThrottled, sourcebudget.ErrDeferred)
		}
		if ctx.Err() != nil {
			return scopedWrap(ScopedTransientProvider, ctx.Err())
		}
		return scopedErrorf(ScopedTransientProvider, "scoped transport failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return scopedErrorf(scopedKindFromAPICode(resp.StatusCode), "scoped HTTP status not successful")
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, (16<<20)+1))
	if err != nil || len(raw) > 16<<20 {
		return scopedErrorf(ScopedTransientProvider, "scoped body invalid or oversized")
	}
	if json.Unmarshal(raw, out) != nil {
		return scopedErrorf(ScopedTransientProvider, "scoped JSON invalid")
	}
	return nil
}
