package repo

import (
	"encoding/base64"
	"fmt"
	"strings"
	"time"
)

// encodePageToken кодирует (created_at, id) в непрозрачный cursor-токен.
func encodePageToken(t time.Time, id string) string {
	raw := fmt.Sprintf("%d:%s", t.UnixNano(), id)
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// decodePageToken декодирует cursor-токен обратно в (time, id).
func decodePageToken(token string) (time.Time, string, error) {
	b, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return time.Time{}, "", err
	}
	parts := strings.SplitN(string(b), ":", 2)
	if len(parts) != 2 {
		return time.Time{}, "", fmt.Errorf("malformed page_token")
	}
	var ns int64
	if _, err := fmt.Sscanf(parts[0], "%d", &ns); err != nil {
		return time.Time{}, "", err
	}
	return time.Unix(0, ns).UTC(), parts[1], nil
}

// sanitizeOrderBy предотвращает SQL-инъекцию в ORDER BY clause.
func sanitizeOrderBy(s string) string {
	allowed := map[string]string{
		"created_at asc":  "created_at ASC, id ASC",
		"created_at desc": "created_at DESC, id DESC",
		"name asc":        "name ASC, id ASC",
		"name desc":       "name DESC, id DESC",
	}
	if v, ok := allowed[strings.ToLower(strings.TrimSpace(s))]; ok {
		return v
	}
	return "created_at ASC, id ASC"
}
