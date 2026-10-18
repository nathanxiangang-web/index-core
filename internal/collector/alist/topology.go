package alist

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"path"
	"sort"
	"strings"
)

// Only this GET endpoint uses the optional topology credential. Do not log the
// response: OpenList's admin envelope may contain unrelated provider secrets.
// Decode and retain only routing facts, never addition/password/token fields.
func (a Adapter) directoryTopology(ctx context.Context, scope string) string {
	if a.TopologyToken == "" || a.ExpectedBasePath == "" {
		return ""
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(a.BaseURL, "/")+"/api/admin/storage/list?page=1&per_page=1001", nil)
	if err != nil {
		return ""
	}
	req.Header.Set("Authorization", a.TopologyToken)
	client := a.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	response, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return ""
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, (16<<20)+1))
	if err != nil || len(raw) > 16<<20 {
		return ""
	}
	defer clear(raw)
	type storage struct {
		ID       int64  `json:"id"`
		Mount    string `json:"mount_path"`
		Status   string `json:"status"`
		Disabled *bool  `json:"disabled"`
		Modified string `json:"modified"`
	}
	var reply struct {
		Code int `json:"code"`
		Data struct {
			Total   *int      `json:"total"`
			Content []storage `json:"content"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &reply) != nil || reply.Code != 200 || reply.Data.Total == nil || *reply.Data.Total != len(reply.Data.Content) || len(reply.Data.Content) > 1000 {
		return ""
	}
	if !strings.HasPrefix(a.ExpectedBasePath, "/") || path.Clean(a.ExpectedBasePath) != a.ExpectedBasePath {
		return ""
	}
	absolute := path.Join(a.ExpectedBasePath, strings.TrimPrefix(scope, "/"))
	cover := false
	seen := map[string]bool{}
	for _, s := range reply.Data.Content {
		if s.ID <= 0 || s.Disabled == nil || !strings.HasPrefix(s.Mount, "/") || path.Clean(s.Mount) != s.Mount || strings.ContainsAny(s.Mount, "\\\x00") || seen[s.Mount] {
			return ""
		}
		seen[s.Mount] = true
		if *s.Disabled {
			continue
		}
		// A nested mount creates virtual children even if the real provider fails.
		if s.Mount != absolute && strings.HasPrefix(s.Mount, strings.TrimSuffix(absolute, "/")+"/") {
			return ""
		}
		if absolute == s.Mount || strings.HasPrefix(absolute, strings.TrimSuffix(s.Mount, "/")+"/") {
			if s.Status != "work" {
				return ""
			}
			cover = true
		}
	}
	if !cover {
		return ""
	}
	sort.Slice(reply.Data.Content, func(i, j int) bool { return reply.Data.Content[i].Mount < reply.Data.Content[j].Mount })
	normalized, _ := json.Marshal(reply.Data.Content)
	digest := sha256.Sum256(normalized)
	return hex.EncodeToString(digest[:])
}
