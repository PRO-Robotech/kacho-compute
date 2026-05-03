package config

import (
	"fmt"
	"time"

	corecfg "github.com/PRO-Robotech/kacho-corelib/config"
)

// SimConfig — параметры симуляции задержек (для тестов и dev).
type SimConfig struct {
	// ProvisionMinMS / ProvisionMaxMS — диапазон задержки PROVISIONING→RUNNING.
	ProvisionMinMS int `envconfig:"KACHO_COMPUTE_SIM_PROVISION_MIN_MS" default:"2000"`
	ProvisionMaxMS int `envconfig:"KACHO_COMPUTE_SIM_PROVISION_MAX_MS" default:"8000"`
	// DiskCreateMinMS / DiskCreateMaxMS — диапазон задержки CREATING→READY для дисков.
	DiskCreateMinMS int `envconfig:"KACHO_COMPUTE_SIM_DISK_CREATE_MIN_MS" default:"2000"`
	DiskCreateMaxMS int `envconfig:"KACHO_COMPUTE_SIM_DISK_CREATE_MAX_MS" default:"10000"`
	// StartStopMinMS / StartStopMaxMS — диапазон задержки START/STOP.
	StartStopMinMS int `envconfig:"KACHO_COMPUTE_SIM_START_STOP_MIN_MS" default:"500"`
	StartStopMaxMS int `envconfig:"KACHO_COMPUTE_SIM_START_STOP_MAX_MS" default:"2000"`
}

// ProvisionDuration возвращает случайную задержку provisioning.
func (s SimConfig) ProvisionDuration() (time.Duration, time.Duration) {
	return time.Duration(s.ProvisionMinMS) * time.Millisecond,
		time.Duration(s.ProvisionMaxMS) * time.Millisecond
}

// DiskCreateDuration возвращает диапазон задержки создания диска.
func (s SimConfig) DiskCreateDuration() (time.Duration, time.Duration) {
	return time.Duration(s.DiskCreateMinMS) * time.Millisecond,
		time.Duration(s.DiskCreateMaxMS) * time.Millisecond
}

// StartStopDuration возвращает диапазон задержки start/stop.
func (s SimConfig) StartStopDuration() (time.Duration, time.Duration) {
	return time.Duration(s.StartStopMinMS) * time.Millisecond,
		time.Duration(s.StartStopMaxMS) * time.Millisecond
}

// Config — конфигурация kacho-compute.
type Config struct {
	DBHost     string `envconfig:"KACHO_COMPUTE_DB_HOST" default:"localhost"`
	DBPort     string `envconfig:"KACHO_COMPUTE_DB_PORT" default:"5432"`
	DBUser     string `envconfig:"KACHO_COMPUTE_DB_USER" default:"compute"`
	DBPassword string `envconfig:"KACHO_COMPUTE_DB_PASSWORD" required:"true"`
	DBName     string `envconfig:"KACHO_COMPUTE_DB_NAME" default:"kacho_compute"`
	GrpcPort   string `envconfig:"KACHO_COMPUTE_GRPC_PORT" default:"9090"`

	ResourceManagerGRPCAddr string `envconfig:"KACHO_COMPUTE_RESOURCE_MANAGER_GRPC_ADDR" default:"resource-manager.kacho.svc.cluster.local:9090"`
	VPCGRPCAddr             string `envconfig:"KACHO_COMPUTE_VPC_GRPC_ADDR" default:"vpc.kacho.svc.cluster.local:9090"`

	Sim SimConfig
}

// DSN возвращает PostgreSQL DSN строку.
func (c Config) DSN() string {
	return fmt.Sprintf(
		"postgres://%s:%s@%s:%s/%s?sslmode=disable",
		c.DBUser, c.DBPassword, c.DBHost, c.DBPort, c.DBName,
	)
}

// Load загружает конфигурацию из переменных окружения.
func Load() (Config, error) {
	var c Config
	err := corecfg.Load(&c)
	return c, err
}
