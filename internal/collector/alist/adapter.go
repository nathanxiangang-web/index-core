// Package alist implements an AList/OpenList Collector adapter over its HTTP API.
// It normalizes to the frozen Snapshot contract and stays additive-safe: AList
// exposes no structured skipped-set here, so skip evidence is UNKNOWN and failure
// visibility is WEAK. The Kernel is never coupled to AList/OpenList internals and
// no upstream source is copied.
package alist

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"strings"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/collector/adapter"
	"github.com/nathanxiangang-web/index-core/internal/domain"
)

// Adapter scans a real AList/OpenList instance via its HTTP API.
type Adapter struct {
	BaseURL  string
	Token    string // optional pre-obtained token
	Username string
	Password string
	Timeout  time.Duration
	HTTP     *http.Client
}

// Scan walks the AList tree under subPath (BFS) and returns normalized evidence.
func (a Adapter) Scan(ctx context.Context, subPath string) (adapter.RawScan, error) {
	if a.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, a.Timeout)
		defer cancel()
	}
	token := a.Token
	if token == "" && a.Username != "" {
		t, err := a.login(ctx)
		if err != nil {
			return failScan(err), nil
		}
		token = t
	}

	root := normalizePath(subPath)
	seen := map[string]bool{}
	queue := []string{root}
	var entries []adapter.NormalizedEntry

	for len(queue) > 0 {
		dir := queue[0]
		queue = queue[1:]
		if seen[dir] {
			continue
		}
		seen[dir] = true

		items, err := a.list(ctx, token, dir)
		if err != nil {
			return failScan(err), nil // honest failure; no partial destructive authority
		}
		for _, it := range items {
			e := adapter.NormalizedEntry{
				EntryLocalID: joinLocal(dir, it.Name),
				Name:         it.Name,
				ParentRef:    dir,
				IsDir:        it.IsDir,
			}
			size := it.Size
			e.Size = &size
			if t, err := time.Parse(time.RFC3339, it.Modified); err == nil {
				utc := t.UTC()
				e.Mtime = &utc
			}
			if alg, sum, ok := pickHash(it.HashInfo); ok {
				e.ContentHash = &sum
				e.HashAlgorithm = &alg
			}
			entries = append(entries, e)
			if it.IsDir {
				queue = append(queue, e.EntryLocalID)
			}
		}
	}

	return adapter.RawScan{
		Entries:                   entries,
		TraversalStatus:           domain.TraversalSuccess,
		SkippedScopes:             nil, // UNKNOWN, never confirmed-empty
		SkippedKnownEmpty:         false,
		Freshness:                 domain.FreshDirect,
		Assurance:                 domain.WeakFailureVisibility,
		ProviderIdentityAssurance: domain.IdentityUnverified,
	}, nil
}

func (a Adapter) login(ctx context.Context) (string, error) {
	body, _ := json.Marshal(map[string]string{"username": a.Username, "password": a.Password})
	var resp apiResp
	if err := a.post(ctx, "/api/auth/login", "", body, &resp); err != nil {
		return "", err
	}
	if resp.Code != http.StatusOK {
		return "", fmt.Errorf("alist login failed: code=%d message=%s", resp.Code, resp.Message)
	}
	var data struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(resp.Data, &data); err != nil {
		return "", err
	}
	if data.Token == "" {
		return "", fmt.Errorf("alist login returned no token")
	}
	return data.Token, nil
}

func (a Adapter) list(ctx context.Context, token, dir string) ([]item, error) {
	body, _ := json.Marshal(map[string]any{"path": dir, "password": "", "page": 1, "per_page": 0, "refresh": false})
	var resp apiResp
	if err := a.post(ctx, "/api/fs/list", token, body, &resp); err != nil {
		return nil, err
	}
	if resp.Code != http.StatusOK {
		return nil, fmt.Errorf("alist list %q failed: code=%d message=%s", dir, resp.Code, resp.Message)
	}
	var data listData
	if err := json.Unmarshal(resp.Data, &data); err != nil {
		return nil, err
	}
	return data.Content, nil
}

func (a Adapter) post(ctx context.Context, path, token string, body []byte, out *apiResp) error {
	base := strings.TrimRight(a.BaseURL, "/")
	if base == "" {
		return fmt.Errorf("alist base URL is required")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", token)
	}
	client := a.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

type apiResp struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

type listData struct {
	Content []item `json:"content"`
	Total   int    `json:"total"`
}

type item struct {
	Name     string            `json:"name"`
	Size     int64             `json:"size"`
	IsDir    bool              `json:"is_dir"`
	Modified string            `json:"modified"`
	Type     int               `json:"type"`
	HashInfo map[string]string `json:"hash_info"`
}

func pickHash(h map[string]string) (algorithm, sum string, ok bool) {
	for _, k := range []string{"sha1", "SHA1", "md5", "MD5"} {
		if v := h[k]; v != "" {
			return strings.ToLower(k), v, true
		}
	}
	return "", "", false
}

func normalizePath(p string) string {
	if p == "" {
		return "/"
	}
	if !strings.HasPrefix(p, "/") {
		return "/" + p
	}
	return p
}

func joinLocal(parent, name string) string {
	if parent == "/" {
		return "/" + name
	}
	return strings.TrimRight(parent, "/") + "/" + name
}

func failScan(err error) adapter.RawScan {
	msg, _ := json.Marshal(map[string]string{"error": err.Error()})
	return adapter.RawScan{
		TraversalStatus:           domain.TraversalFailed,
		ErrorSummary:              msg,
		SkippedScopes:             nil,
		SkippedKnownEmpty:         false,
		Freshness:                 domain.FreshDirect,
		Assurance:                 domain.WeakFailureVisibility,
		ProviderIdentityAssurance: domain.IdentityUnverified,
	}
}
