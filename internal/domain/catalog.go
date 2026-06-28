// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package domain

import "time"

// DiskType — тип диска (глобальный read-only справочник; id = "network-ssd").
type DiskType struct {
	ID          string
	Description string
	ZoneIDs     []string
	CreatedAt   time.Time
}
