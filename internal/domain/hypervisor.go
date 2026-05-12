package domain

import "time"

// HypervisorState — состояние гипервизора (зеркалит computev1.Hypervisor_State).
type HypervisorState int

// Значения HypervisorState.
const (
	HypervisorStateUnspecified HypervisorState = iota
	HypervisorStateReady
	HypervisorStateCordoned
	HypervisorStateDraining
	HypervisorStateDown
)

// HypervisorCapacity — ёмкость хоста (для placement-решений).
type HypervisorCapacity struct {
	VCPUs       int64
	MemoryBytes int64
	Instances   int64
}

// Hypervisor — физический хост, на котором kacho-compute размещает инстансы.
// INTERNAL-ONLY: на публичной поверхности не появляется (см. workspace CLAUDE.md
// §«Инфра-чувствительные данные»). node_index — стабильный индекс узла, основа
// /48-SRv6-локатора хоста в kacho-vpc-implement.
type Hypervisor struct {
	ID        string
	ZoneID    string
	NodeIndex uint32
	FQDN      string
	State     HypervisorState
	Capacity  HypervisorCapacity
	CreatedAt time.Time
	UpdatedAt time.Time
}
