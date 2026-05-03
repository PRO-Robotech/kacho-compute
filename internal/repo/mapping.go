package repo

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

func uuidToStr(u pgtype.UUID) string {
	if !u.Valid {
		return ""
	}
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		u.Bytes[0:4], u.Bytes[4:6], u.Bytes[6:8], u.Bytes[8:10], u.Bytes[10:16])
}

func strToUUID(s string) (pgtype.UUID, error) {
	var u pgtype.UUID
	if err := u.Scan(s); err != nil {
		return pgtype.UUID{}, err
	}
	return u, nil
}

// strToUUIDOrZero возвращает нулевой UUID, если строка пустая.
// Используется для cloud_id / organization_id, которые могут быть неизвестны
// на этапе sub-phase 0.4 (иерархия Org→Cloud→Folder ещё не передаётся).
func strToUUIDOrZero(s string) pgtype.UUID {
	if s == "" {
		return pgtype.UUID{Valid: true} // all-zero bytes, but Valid=true → NOT NULL satisfied
	}
	u, err := strToUUID(s)
	if err != nil {
		return pgtype.UUID{Valid: true}
	}
	return u
}

func tsToTime(ts pgtype.Timestamptz) time.Time {
	if ts.Valid {
		return ts.Time
	}
	return time.Time{}
}

func tsToTimePtr(ts pgtype.Timestamptz) *time.Time {
	if ts.Valid {
		t := ts.Time
		return &t
	}
	return nil
}

func jsonbToMap(b []byte) map[string]string {
	if len(b) == 0 {
		return map[string]string{}
	}
	m := map[string]string{}
	_ = json.Unmarshal(b, &m)
	return m
}

func mapToJSONB(m map[string]string) []byte {
	if len(m) == 0 {
		b, _ := json.Marshal(map[string]string{})
		return b
	}
	b, _ := json.Marshal(m)
	return b
}

func jsonbToAny(b []byte, v any) {
	if len(b) > 0 {
		_ = json.Unmarshal(b, v)
	}
}

func anyToJSONB(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

// nonNilStrings возвращает пустой срез вместо nil, чтобы pgx
// передавал '{}' в NOT NULL TEXT[] колонку вместо NULL.
func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func timePtrToTS(t *time.Time) pgtype.Timestamptz {
	if t == nil {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: *t, Valid: true}
}
