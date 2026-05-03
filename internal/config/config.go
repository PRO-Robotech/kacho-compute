package config

import (
	"fmt"

	corecfg "github.com/PRO-Robotech/kacho-corelib/config"
	"github.com/PRO-Robotech/kacho-compute/internal/reconciler"
)

// Config содержит конфигурацию сервиса kacho-compute.
type Config struct {
	DBHost     string `envconfig:"KACHO_COMPUTE_DB_HOST" default:"localhost"`
	DBPort     string `envconfig:"KACHO_COMPUTE_DB_PORT" default:"5432"`
	DBUser     string `envconfig:"KACHO_COMPUTE_DB_USER" default:"compute"`
	DBPassword string `envconfig:"KACHO_COMPUTE_DB_PASSWORD" required:"true"`
	DBName     string `envconfig:"KACHO_COMPUTE_DB_NAME" default:"kacho_compute"`
	GrpcPort   string `envconfig:"KACHO_COMPUTE_GRPC_PORT" default:"9092"`

	// Адреса зависимых сервисов
	ResourceManagerGRPCAddr string `envconfig:"KACHO_COMPUTE_RESOURCE_MANAGER_GRPC_ADDR" default:"resource-manager:9090"`
	VPCGRPCAddr             string `envconfig:"KACHO_COMPUTE_VPC_GRPC_ADDR" default:"vpc:9091"`

	// Симулированные задержки reconciler-а
	Sim reconciler.SimConfig
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
