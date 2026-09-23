package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"errors"

	"github.com/nathanxiangang-web/index-core/internal/query"
)

// ErrInvalidCursor is returned for an undecodable cursor token.
var ErrInvalidCursor = errors.New("invalid cursor")

// encodeCursor serializes the generation-bound cursor opaquely for HTTP.
func encodeCursor(c *query.Cursor) string {
	if c == nil {
		return ""
	}
	b, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(b)
}

// decodeCursor parses an opaque cursor token back into a query.Cursor.
func decodeCursor(token string) (*query.Cursor, error) {
	if token == "" {
		return nil, nil
	}
	b, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return nil, ErrInvalidCursor
	}
	var c query.Cursor
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, ErrInvalidCursor
	}
	return &c, nil
}
