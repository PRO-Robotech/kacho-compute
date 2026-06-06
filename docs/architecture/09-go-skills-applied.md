# 09 — Go Skills Applied

Краткая карта того, какие практики `golang-*` скилов закладываются в kacho-compute.
Сервис написан на проверенных паттернах `kacho-vpc` — детальный trail-документ
там (`../kacho-vpc/docs/architecture/09-go-skills-applied.md`); здесь — что
актуально для compute-домена.

| Skill | Как применяется в kacho-compute |
|---|---|
| `golang-project-layout` | Clean Architecture: `cmd/compute/main.go` (composition root) + `internal/{domain,service,ports,repo,handler,clients,protoconv,migrations,config}`. Зеркало layout'а всех kacho-* сервисов. |
| `golang-dependency-injection` / `golang-structs-interfaces` | Manual constructor injection в `cmd/compute/main.go` (composition root). Port-интерфейсы (`DiskRepo`, `ImageRepo`, `SnapshotRepo`, `InstanceRepo`, `DiskTypeRepo`, `ZoneRepo`/`ZoneRegistry`, `OperationsRepo`, `FolderClient`, `VPCClient`) определены в `internal/service`, реализованы в `internal/repo` (pgx) и `internal/clients` (gRPC). «Accept interfaces, return structs». Composition вместо embedding. Без DI-framework — `samber/do`/`uber/dig`/`uber/fx`/`google/wire` overkill для 6-ресурсного сервиса. Compile-time interface-check (`var _ DiskRepo = (*diskRepo)(nil)`). |
| `golang-error-handling` | Sentinel-ошибки в leaf-пакете `internal/ports` (`ErrNotFound` / `ErrAlreadyExists` / `ErrFailedPrecondition` / `ErrInvalidArg` / `ErrInternal`) — чтобы `portmock` мог их возвращать без import-cycle. `fmt.Errorf` с `%w` для wrapping. `internal/service/maperr.go::mapRepoErr` — единая точка трансляции sentinel → gRPC-status с verbatim YC текстом; `stripSentinel` убирает internal-обёртку; `status.FromError + code != Unknown` guard не маппит повторно. Никакого pgx-текста наружу (`ErrInternal` → `"internal database error"`). |
| `golang-context` | `context.Context` первым параметром везде; распространяется в repo (pgx) и clients (gRPC). `context.Background()` только в shutdown-cleanup (отписка от LISTEN). Никакого `context.TODO()` в production-коде. Worker-горутины (`operations.Run`) получают свой ctx с cancel на shutdown. |
| `golang-design-patterns` | Worker pattern — `kacho-corelib/operations.Run(ctx, opsRepo, opID, fn)` для всех LRO. Outbox pattern — событие в `compute_outbox` в той же TX, что domain-write. Retry-on-conflict — `xmin` OCC для `UpdateNetworkInterface`. Functional options не используются (constructors короткие). Graceful shutdown — `cmd/compute/main.go` вызывает `operations.Wait(30s)` перед закрытием pool'ов (закрытый concurrency P0 #1 в corelib: WaitGroup + recover в worker). |
| `golang-observability` | Структурное логирование `slog` (JSON handler из `kacho-corelib/observability`) — стандарт. **Gap** (как у VPC): Prometheus metrics / OpenTelemetry trace / pprof endpoint пока не подключены → GitHub Issue (observability gap). |
| `golang-grpc` | `grpcsrv` из corelib (recovery + logging interceptors). Два gRPC-сервера: `:9090` public (`InstanceService`/`DiskService`/`ImageService`/`SnapshotService`/`DiskTypeService`/`ZoneService`/`OperationService`), `:9091` internal (`InternalWatchService`/`InternalDiskTypeService`/`InternalZoneService`). `status.FromError`-mapping в handler через `mapRepoErr`. Streaming только в `InternalWatchService.Watch` (один-к-одному с pgx LISTEN/NOTIFY). Plaintext в dev, TLS-флаги для cross-service (`KACHO_COMPUTE_VPC_TLS`, `KACHO_COMPUTE_RESOURCE_MANAGER_TLS`). |
| `golang-database` | pgx без ORM (workspace `CLAUDE.md` §запрет 3 — только sqlc + handwritten pgx). `tx.Begin/Commit` с `defer Rollback`. Prepared statements через pgx auto-prepare. Outbox INSERT в той же TX, что domain INSERT/UPDATE/DELETE. FK constraints (`attached_disks.disk_id` RESTRICT, `.instance_id` CASCADE; `instance_network_interfaces.instance_id` CASCADE) + partial UNIQUE `(folder_id, name) WHERE name <> ''` — race-free на DB-level. `xmin::text` для OCC. `pool_max_conns` только в pgxpool DSN (не в `migrate` DSN — иначе `database/sql` FATAL на unknown PG-param). |
| `golang-testing` / `golang-stretchr-testify` | Test pyramid: unit (`internal/service/*_test.go`, `internal/handler/*_test.go`) через моки port-интерфейсов из `internal/ports/portmock`; worker-горутины дожидаются детерминированно через `portmock.AwaitOpDone`/`AwaitAllOpsDone` (poll до `Operation.Done`, дедлайн 2s — не `time.Sleep`). Integration (`internal/repo/*integration_test.go`) — testcontainers Postgres 16 (локально `make test` + CI job `integration`): Repo CRUD, partial UNIQUE `(folder_id, name)`, FK `attached_disks` (attach/detach + delete-blocked + Instance.Delete cascade), outbox emit транзакционность + LISTEN/NOTIFY, Instance NIC cascade delete, xmin OCC, `regions`/`zones` FK RESTRICT. E2E (`tests/newman/`) — black-box через api-gateway, декларативные `cases/*.py` → `gen.py` → Postman-коллекции; кейсы (эпик `KAC-15`): **Region/Zone** (list/get/admin-CRUD/del-restrict/not-in-vpc). ⚠️ кейсы «Instance с `nic_id`» (attach existing / inline-create NIC) сняты — авто-NIC материализация удалена в `KAC-266` (инстанс создаётся без сетевых интерфейсов; правильная сетевая модель — будущая переделка). testify — пока stdlib `testing`; миграция не приоритетна. |
| `golang-naming` | MixedCaps + acronym rules. Proto-mirror naming (`IpVersion`, `OneToOneNat`, `SetXxxId`) сохранён — переименование сломало бы proto-API. Constructors `NewXxxService`. Sentinel-ошибки `ErrXxx`. Test-функции `TestXxx_Yyy` / table-driven с `name`-полем. |
| `golang-code-style` / `golang-modernize` / `golang-lint` | gofmt clean. `.golangci.yml` v2 (errcheck + govet + ineffassign + staticcheck + unused + misspell + revive + bodyclose + copyloopvar) — 0 issues цель. Код на Go 1.22+. |
| `golang-continuous-integration` | `.github/workflows/ci.yaml` — build + vet + test-race + lint + govulncheck + integration (testcontainers). dependabot.yml. CI временно пиннит siblings к feature-веткам (`ref:`-строки) пока кросс-репо изменения не в `main`. |
| `golang-safety` | Нет defer-in-loop в hot-path; nil-deref защита на cross-service client'ах (`SkipPeerValidation` → no-op-клиент, не nil); numeric conversions проверяются (size/cores int64 boundaries). |
| `golang-security` | TLS terminate в api-gateway. `KACHO_COMPUTE_DB_SSLMODE` (default `disable` для dev), `KACHO_COMPUTE_*_TLS` для cross-service. `KACHO_COMPUTE_AUTH_MODE` (`dev`/`production`/`production-strict`) — fail-closed гейт перед IAM merge. **Gap**: полноценный IAM/AuthZ (claims-extraction, folder-membership), mTLS на `:9091`, NetworkPolicy для internal-port — scope (как у VPC). |
| `golang-benchmark` / `golang-performance` | Hot-path кандидаты на бенчмарки: `Disk.Create` worker (folder/zone/type/source checks → INSERT), `Instance.Create` worker (folder/zone checks → INSERT; per-NIC vpcClient round-trips сняты — авто-NIC удалён в `KAC-266`), `mapRepoErr` (cheap). N+1 не должно быть в `List` (cursor pagination, один SELECT). |

## Skipped (с обоснованием) — как у VPC

`golang-popular-libraries` / `golang-stay-updated` (не refactor — discovery);
`golang-dependency-management` (покрыт dependabot); `golang-graphql` (у нас gRPC);
`golang-google-wire` / `golang-uber-dig` / `golang-uber-fx` / `golang-samber-do`
(manual constructor injection достаточен); `golang-samber-lo` / `golang-samber-mo`
/ `golang-samber-ro` / `golang-samber-hot` / `golang-samber-oops` /
`golang-samber-slog` (новые библиотеки без real need — sentinel + `%w` + stdlib
slog достаточны); `golang-spf13-viper` (corelib/config — envconfig — покрывает);
`golang-spf13-cobra` / `golang-cli` (у сервиса нет CLI кроме `migrate` subcmd —
минимальный `flag`); `golang-swagger` (grpc-gateway генерит OpenAPI);
`golang-troubleshooting` (pprof endpoint пока не подключён — observability gap).

## Open gaps (общие с kacho-vpc, заводятся как GitHub Issues)

- Prometheus metrics / OpenTelemetry distributed tracing / pprof endpoint —
  observability gap.
- Полноценный IAM (claims-extraction / folder-membership через resource-manager);
  mTLS на `:9091`; NetworkPolicy для internal-port.
- Integration-test run в CI (скелет есть; testcontainers job стабилизировать).
- `operations.Run` heartbeat/cleanup для зависших операций при краше pod'а
  (общий для всех kacho-* — `kacho-corelib/operations`).
