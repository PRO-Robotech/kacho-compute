package domain_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/PRO-Robotech/kacho-compute/internal/domain"
)

func TestDisk_Size2Bytes(t *testing.T) {
	tests := []struct {
		size  string
		want  int64
	}{
		{"10Gi", 10 * 1024 * 1024 * 1024},
		{"100Gi", 100 * 1024 * 1024 * 1024},
		{"1Ti", 1024 * 1024 * 1024 * 1024},
		{"512Mi", 512 * 1024 * 1024},
		{"invalid", 0},
		{"", 0},
	}
	for _, tt := range tests {
		t.Run(tt.size, func(t *testing.T) {
			d := &domain.Disk{Size: tt.size}
			assert.Equal(t, tt.want, d.Size2Bytes())
		})
	}
}
