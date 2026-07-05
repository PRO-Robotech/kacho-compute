// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

// Package main — отдельный binary `kacho-migrator`: CLI управления миграциями
// схемы БД compute (goose поверх embed `internal/migrations`).
//
//	kacho-migrator up      # прокатить все pending-миграции
//	kacho-migrator down    # откатить последнюю миграцию
//	kacho-migrator status  # показать применённые/pending
//
// Отдельная точка сборки (зеркалит kacho-vpc / kacho-iam): serve-binary
// `kacho-compute` больше НЕ несёт embed-миграции и деструктивный `migrate down`
// (least-privilege — runtime-образ не может менять схему live-БД). Миграции
// гоняет отдельный one-shot init-container/Job с этим бинарём.
//
// DSN берётся из того же config.Load() (viper/env), что и serve — одно
// helm-values задаёт БД-параметры для обоих бинарей.
package main

import (
	"database/sql"
	"log"
	"os"

	_ "github.com/jackc/pgx/v5/stdlib" // регистрирует "pgx" driver для sql.Open
	"github.com/pressly/goose/v3"

	"github.com/PRO-Robotech/kacho-compute/internal/config"
	"github.com/PRO-Robotech/kacho-compute/internal/migrations"
)

func main() {
	if len(os.Args) < 2 {
		log.Fatal("usage: kacho-migrator {up|down|status}")
	}
	direction := os.Args[1]

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("postgres"); err != nil {
		log.Fatalf("goose dialect: %v", err)
	}
	db, err := sql.Open("pgx", cfg.MigrateDSN())
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer func() { _ = db.Close() }()

	var gooseErr error
	switch direction {
	case "up":
		gooseErr = goose.Up(db, ".")
	case "down":
		gooseErr = goose.Down(db, ".")
	case "status":
		gooseErr = goose.Status(db, ".")
	default:
		log.Fatalf("unknown command %q (usage: kacho-migrator {up|down|status})", direction)
	}
	if gooseErr != nil {
		log.Fatalf("migrate %s: %v", direction, gooseErr)
	}
}
