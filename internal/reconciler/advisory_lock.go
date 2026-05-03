package reconciler

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// tryAdvisoryLock пытается взять pg_try_advisory_lock для данного ресурса.
// Возвращает true если лок взят, false если уже занят другой репликой.
func tryAdvisoryLock(ctx context.Context, pool *pgxpool.Pool, resourceUID string) (bool, error) {
	var acquired bool
	err := pool.QueryRow(ctx,
		`SELECT pg_try_advisory_lock(hashtext($1))`,
		fmt.Sprintf("compute_%s", resourceUID),
	).Scan(&acquired)
	return acquired, err
}

// advisoryUnlock освобождает pg_advisory_lock.
func advisoryUnlock(ctx context.Context, pool *pgxpool.Pool, resourceUID string) {
	_, _ = pool.Exec(ctx,
		`SELECT pg_advisory_unlock(hashtext($1))`,
		fmt.Sprintf("compute_%s", resourceUID),
	)
}
