package repo

// scannable — общий интерфейс над pgx.Row / pgx.Rows для scan-helper'ов.
type scannable interface {
	Scan(dest ...any) error
}
