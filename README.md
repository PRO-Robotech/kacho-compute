# kacho-compute

Compute-сервис Kachō: control-plane для **Instance, Disk, Image, Snapshot** +
read-only справочники **DiskType, Region, Zone** (Geography — owner kacho-compute,
эпик `KAC-15`) + internal-only инфра-реестр **Hypervisor** (физические хосты;
placement / HW инвентарь — на публичной поверхности не появляется). Compute-NIC
бэкуется ресурсом kacho-vpc `NetworkInterface` (`nic_id`, эпик `KAC-9`).
Подробности — `CLAUDE.md` и `docs/architecture/`.

## Quick start (локальный стенд)

```bash
# 1. Поднять полный стенд (kind + helm + Postgres + все сервисы)
cd ../kacho-deploy && make dev-up

# 2. Прокинуть api-gateway наружу
kubectl -n kacho port-forward svc/api-gateway 18080:8080 &

# 3. Smoke
curl 'http://localhost:18080/compute/v1/diskTypes'
curl 'http://localhost:18080/compute/v1/regions'
curl 'http://localhost:18080/compute/v1/zones'
curl 'http://localhost:18080/compute/v1/disks?folderId=<folder>&pageSize=5'
# Hypervisor — internal-only: доступен ТОЛЬКО на cluster-internal listener,
# на external TLS endpoint GET /compute/v1/hypervisors → 404
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
| `:9090`| `InstanceService`, `DiskService`, `ImageService`, `SnapshotService`, `DiskTypeService`, `RegionService`, `ZoneService`, `OperationService` | api-gateway (external + UI) |
| `:9091`| `InternalWatchService`, `InternalDiskTypeService`, `InternalRegionService`, `InternalZoneService`, `InternalHypervisorService` (синхронные RPC — infra-registry) | admin-tooling / UI (через api-gateway internal mux) — НЕ на external TLS endpoint; `GET /compute/v1/hypervisors` на external → 404 |

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
