package service

func normalizeMap(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}
