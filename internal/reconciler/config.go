package reconciler

// SimConfig содержит параметры симулированных задержек reconciler-а.
// Все значения — в миллисекундах.
type SimConfig struct {
	InstanceProvisionMinMs int `envconfig:"KACHO_COMPUTE_SIM_PROVISION_MIN_MS" default:"5000"`
	InstanceProvisionMaxMs int `envconfig:"KACHO_COMPUTE_SIM_PROVISION_MAX_MS" default:"30000"`
	InstanceStartMinMs     int `envconfig:"KACHO_COMPUTE_SIM_START_MIN_MS" default:"5000"`
	InstanceStartMaxMs     int `envconfig:"KACHO_COMPUTE_SIM_START_MAX_MS" default:"15000"`
	InstanceStopMinMs      int `envconfig:"KACHO_COMPUTE_SIM_STOP_MIN_MS" default:"5000"`
	InstanceStopMaxMs      int `envconfig:"KACHO_COMPUTE_SIM_STOP_MAX_MS" default:"15000"`
	DiskCreateMinMs        int `envconfig:"KACHO_COMPUTE_SIM_DISK_CREATE_MIN_MS" default:"3000"`
	DiskCreateMaxMs        int `envconfig:"KACHO_COMPUTE_SIM_DISK_CREATE_MAX_MS" default:"10000"`
	SnapshotMinMs          int `envconfig:"KACHO_COMPUTE_SIM_SNAPSHOT_MIN_MS" default:"10000"`
	SnapshotMaxMs          int `envconfig:"KACHO_COMPUTE_SIM_SNAPSHOT_MAX_MS" default:"30000"`
}

// DefaultSimConfig возвращает конфигурацию по умолчанию.
func DefaultSimConfig() SimConfig {
	return SimConfig{
		InstanceProvisionMinMs: 5000,
		InstanceProvisionMaxMs: 30000,
		InstanceStartMinMs:     5000,
		InstanceStartMaxMs:     15000,
		InstanceStopMinMs:      5000,
		InstanceStopMaxMs:      15000,
		DiskCreateMinMs:        3000,
		DiskCreateMaxMs:        10000,
		SnapshotMinMs:          10000,
		SnapshotMaxMs:          30000,
	}
}

// TestSimConfig возвращает конфигурацию для integration-тестов (100-200 мс).
func TestSimConfig() SimConfig {
	return SimConfig{
		InstanceProvisionMinMs: 100,
		InstanceProvisionMaxMs: 200,
		InstanceStartMinMs:     100,
		InstanceStartMaxMs:     200,
		InstanceStopMinMs:      100,
		InstanceStopMaxMs:      200,
		DiskCreateMinMs:        100,
		DiskCreateMaxMs:        200,
		SnapshotMinMs:          100,
		SnapshotMaxMs:          400,
	}
}
