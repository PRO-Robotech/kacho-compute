package domain

import "strings"

// Size2Bytes конвертирует "10Gi" / "100Gi" в приблизительное количество байт.
// Используется для заполнения Snapshot.Size.
func (d *Disk) Size2Bytes() int64 {
	s := d.Size
	switch {
	case strings.HasSuffix(s, "Ti"):
		return parseNum(s[:len(s)-2]) * 1024 * 1024 * 1024 * 1024
	case strings.HasSuffix(s, "Gi"):
		return parseNum(s[:len(s)-2]) * 1024 * 1024 * 1024
	case strings.HasSuffix(s, "Mi"):
		return parseNum(s[:len(s)-2]) * 1024 * 1024
	default:
		return 0
	}
}

func parseNum(s string) int64 {
	var n int64
	for _, c := range s {
		if c >= '0' && c <= '9' {
			n = n*10 + int64(c-'0')
		}
	}
	return n
}
