package migrations

import "embed"

// FS — embedded боевые миграции kacho-compute (goose-формат).
// Source of truth — этот каталог; migrations/ в корне репо — staging для
// `make sync-migrations` (только 0001_operations.sql от corelib; в 0001_initial.sql
// схема operations уже включена в baseline).
//
//go:embed *.sql
var FS embed.FS
