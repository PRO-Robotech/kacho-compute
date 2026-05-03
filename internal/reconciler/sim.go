package reconciler

import (
	"math/rand/v2"
	"time"
)

// randDuration возвращает случайную длительность в диапазоне [min, max].
func randDuration(min, max time.Duration) time.Duration {
	if min >= max {
		return min
	}
	delta := int64(max - min)
	return min + time.Duration(rand.Int64N(delta))
}
