package migrations

import "embed"

// FS содержит все SQL миграции goose.
//
//go:embed *.sql
var FS embed.FS
