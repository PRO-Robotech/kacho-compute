// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package repo

// scannable — общий интерфейс над pgx.Row / pgx.Rows для scan-helper'ов.
type scannable interface {
	Scan(dest ...any) error
}
