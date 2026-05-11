# kacho-compute

Compute-сервис Kachō: control-plane для **Instance, Disk, Image, Snapshot** +
read-only справочники **DiskType, Zone**. Цель — verbatim parity с Yandex Cloud
Compute API (`kacho.cloud.compute.v1` == зеркало `yandex.cloud.compute.v1`).
Подробности — `CLAUDE.md` (sub-phase 0.4) и `docs/architecture/`.

## Quick start (локальный стенд)

```bash
# 1. Поднять полный стенд (kind + helm + Postgres + все сервисы)
cd ../kacho-deploy && make dev-up

# 2. Прокинуть api-gateway наружу
kubectl -n kacho port-forward svc/api-gateway 18080:8080 &

# 3. Smoke
curl 'http://localhost:18080/compute/v1/diskTypes'
curl 'http://localhost:18080/compute/v1/zones'
curl 'http://localhost:18080/compute/v1/disks?folderId=<folder>&pageSize=5'
```

Перезапуск только compute после изменений в коде:
```bash
cd ../kacho-deploy && make reload-svc SVC=compute
make logs-svc SVC=compute        # tail логов
make psql SVC=compute            # psql kacho_compute
```

## Архитектура

Clean Architecture (`domain → service → handler/repo/clients`); `cmd/compute/main.go` —
единственный composition root. Все мутации (`Create/Update/Delete/Start/Stop/...`)
возвращают `Operation` (LRO), выполнение worker'ом через
`kacho-corelib/operations.Run`. Outbox + LISTEN/NOTIFY дают event stream через
`InternalWatchService` (для admin-tooling / UI). Подробности по слоям и
паттернам — `CLAUDE.md` §4 и `docs/architecture/`.

### Dual gRPC ports

| Порт   | Сервисы                                                                  | Кто использует                  |
|--------|--------------------------------------------------------------------------|----------------------------------|
| `:9090`| `InstanceService`, `DiskService`, `ImageService`, `SnapshotService`, `DiskTypeService`, `ZoneService`, `OperationService` | api-gateway (external + UI) |
| `:9091`| `InternalWatchService`, `InternalDiskTypeService`, `InternalZoneService` | admin-tooling / UI (через api-gateway internal mux) — НЕ на external TLS endpoint |

## Тесты

```bash
make test-short    # unit (service/handler) + -short
make test          # unit + integration (testcontainers Postgres 16)
python3 tests/newman/scripts/gen.py && tests/newman/scripts/run.sh   # E2E (нужен port-forward api-gateway)
```

Три уровня: unit (`internal/service|handler/*_test.go`, моки port-интерфейсов из
`internal/ports/portmock`), integration (`internal/repo/*integration_test.go`,
testcontainers), e2e (`tests/newman/`, декларативные `cases/*.py` → `gen.py` →
Postman-коллекции). Критерий приёмки: любой newman-кейс зеленеет и против
реального YC Compute API.

## Полезное

- Открытые задачи / баги: GitHub Issues (`TODO.md` упразднён).
- By-design расхождения с YC: `docs/architecture/07-known-divergences.md`.
- Proto: `../kacho-proto/proto/kacho/cloud/compute/v1/`.
- Эталон-паттерны: `../kacho-vpc/` (compute написан на них).
