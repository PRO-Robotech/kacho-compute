package ports

import "errors"

// Sentinel-ошибки слоя service/repo. Живут здесь (в leaf-пакете ports), а не в
// `internal/service`, чтобы общий test-helper `internal/ports/portmock` мог их
// возвращать без зависимости от `internal/service` (иначе — import-cycle с
// white-box service-тестами). `internal/service` ре-экспортирует их через
// type-alias'ы (`var ErrNotFound = ports.ErrNotFound` — тот же error-value, так
// что `errors.Is` работает прозрачно). Зеркалит kacho-vpc/internal/ports.
var (
	// ErrNotFound возвращается, когда ресурс не найден.
	ErrNotFound = errors.New("not found")
	// ErrAlreadyExists возвращается при нарушении UNIQUE constraint.
	ErrAlreadyExists = errors.New("already exists")
	// ErrInvalidArg возвращается при некорректных входных данных.
	ErrInvalidArg = errors.New("invalid argument")
	// ErrFailedPrecondition возвращается, когда операция отклонена из-за
	// состояния ресурса (например, удаление Disk пока он attached — нарушение
	// FK 23503). Маппится в gRPC FailedPrecondition (verbatim YC).
	ErrFailedPrecondition = errors.New("failed precondition")
	// ErrInternal — generic-ошибка для неклассифицированных DB-проблем.
	// Маппится на gRPC Internal с фиксированным сообщением (no leak).
	ErrInternal = errors.New("internal database error")
)
