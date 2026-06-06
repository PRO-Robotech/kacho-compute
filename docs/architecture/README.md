# kacho-compute — Architecture

Архитектурная документация именно по Compute-сервису. Workspace-уровень (как
он связан с другими сервисами, общий стек, общие правила) — в
`kacho-workspace/CLAUDE.md` и `kacho-workspace/docs/specs/`.

> **Итоговый самодостаточный документ** — [`../ARCHITECTURE.md`](../ARCHITECTURE.md).
> Документы ниже — детализация по конкретным темам.
>
> Происхождение сервиса: написан заново на проверенных паттернах `kacho-vpc`
> (flat resources + Operations LRO + Clean Architecture + verbatim YC parity).
> Где видишь «как в VPC» — буквально смотри одноимённый файл в `../kacho-vpc/`.

## Содержание

| # | Документ | О чём |
|---|---|---|
| 00 | [Overview](00-overview.md) | Что делает kacho-compute, какие ресурсы owns, что вне скоупа, 6 принципов проекта, Clean Architecture, verbatim-YC parity goal |
| 01 | [Resources](01-resources.md) | Детально по каждому ресурсу: Disk, Image, Snapshot, Instance (+`nic_id`→kacho-vpc NIC), DiskType, Region, Zone (Geography, owner kacho-compute) — proto-поля, ID-префиксы, status-enum'ы, полный список RPC с пометкой implemented/blocked/Unimplemented, инварианты, cross-resource links |
| 02 | [Data Flows](02-data-flows.md) | Sequence-диаграммы compute-сценариев: Operations LRO worker, Disk.Create, Image.Create (source oneof), Snapshot.Create, Instance.Create (NIC/boot-disk validation), AttachDisk/DetachDisk, outbox + LISTEN/NOTIFY + InternalWatchService |
| 03 | [Instance Lifecycle](03-instance-lifecycle.md) | State-машина `Instance.Status` (PROVISIONING/RUNNING/STOPPING/STOPPED/STARTING/RESTARTING/UPDATING/ERROR/CRASHED/DELETING), transition-таблица (RPC × precondition × end-status × Operation.response), control-plane имитация, AttachDisk/DetachDisk/NAT инварианты |
| 04 | [API Surface](04-api-surface.md) | Таблица всех публичных RPC (REST path, method, request/response, Operation metadata/response, sync-vs-async, implemented/blocked) + internal RPC (InternalWatchService / InternalDiskTypeService / InternalRegionService / InternalZoneService на :9091) |
| 05 | [Database](05-database.md) | Схема `kacho_compute`, миграции (`0003_geography_owner.sql` — regions+zones owned by compute; `0005_instance_nic_id.sql` — `instance_network_interfaces.nic_id`): все таблицы, колонки, индексы, FK, partial UNIQUE, outbox trigger, seed, flat-схема, xmin OCC |
| 06 | [Conventions & Gotchas](06-conventions.md) | Compute-specific правила: naming, error mapping, timestamp truncation, UpdateMask discipline, pagination, filter, hard-delete, Operation prefix `epd`, cross-service ref-validation, `nic_id`-on-Instance, Geography owner=compute, `KACHO_COMPUTE_SKIP_PEER_VALIDATION` |
| 07 | [Намеренные решения / расхождения с YC](07-known-divergences.md) | Реестр осознанных by-design решений (НЕ баги; verbatim-parity отложена) — id-syntax validation, name-policy probe, Instance precondition тексты, control-plane имитация, DiskType/Region/Zone admin-CRUD, Geography owner=kacho-compute (KAC-15), Instance NIC ↔ kacho-vpc NetworkInterface (KAC-9), blocked-on-missing-service |
| 08 | [UI](08-ui.md) | Интеграция с `kacho-ui` (Vite + React SPA): compute-views (Instances/Disks/Images/Snapshots), generic CRUD-страницы, polling Operation, attach/detach disk, Start/Stop/Restart actions, DiskType/Zone dropdowns — forward-looking design |
| 09 | [Go skills applied](09-go-skills-applied.md) | Какие практики `golang-*` скилов применены: clean architecture / DI, error handling, context propagation, graceful shutdown, slog, testing pyramid, naming, grpc, pgx-database |

## TL;DR — что это за сервис

Sub-phase 0.4 продукта Kachō. gRPC-сервис управления вычислительными ресурсами:
**Instance, Disk, Image, Snapshot** + read-only справочники **DiskType, Zone**.
Цель — verbatim parity с Yandex Cloud Compute API (`kacho.cloud.compute.v1`
== зеркало `yandex.cloud.compute.v1`): proto-форма, error texts, status codes,
timestamp precision, regex'ы, behavioural semantics, state-машина Instance.

Owns:
- 4 мутируемых folder-level ресурса: Disk, Image, Snapshot, Instance (NIC бэкуется
  kacho-vpc `NetworkInterface` через `nic_id` — эпик `KAC-9`).
- read-only справочники: DiskType; **Region, Zone** (Geography — owner kacho-compute,
  эпик `KAC-15`: перенесено из kacho-vpc, нет proxy / `skipPeer`-fallback).
- `operations` таблица (per-сервисная, prefix `epd`).
- in-process outbox + LISTEN/NOTIFY → `InternalWatchService`.
- `InternalDiskTypeService` / `InternalRegionService` / `InternalZoneService` — kacho-only
  admin CRUD справочников.

Control plane only: реального data-plane нет — `Instance.status` переходит
детерминированной state-машиной внутри worker'а соответствующей операции;
disk data не существует; serial-port output синтетический; image download
(uri-source) мгновенный.

## Связь с другими репо

```
       kacho-ui (SPA, REST/JSON)
              |
              v
       kacho-api-gateway
       /      |          \
      v       v public    v internal :9091
   resource  vpc       kacho-compute
   -manager  :9090     ┌─────────────────┐
   (folder)  (subnet/  │  service layer  │
             SG/addr)  └─┬───────┬───────┘
        ^         ^      │       │ folderClient
        └─────────┼──────┘       └──→ resource-manager.FolderService.Exists
                  │ vpcClient (Subnet/SecurityGroup/Address .Get)
                  v
            pg-compute (своя БД kacho_compute)
```

Внешние зависимости:
- `kacho-resource-manager.FolderService.Exists` — existence-check `folder_id`
  в Create/Move (как в VPC).
- `kacho-vpc.{SubnetService.Get, SecurityGroupService.Get, AddressService.Get}` —
  валидация `network_interface_spec` Instance'а.
- `kacho-corelib` — `ids`, `operations`, `db`, `grpcsrv`, `grpcclient`, `outbox`,
  `validate`, `filter`, `retry`, `shutdown`, `observability`, `config`, `errors`.
- `kacho-proto` — все `.proto`, generated stubs (`gen/go/kacho/cloud/compute/v1`).

kacho-compute **не знает** про:
- api-gateway (просто слушает 9090/9091).
- UI/CLI (это REST/gRPC потребители).
- другие kacho-* (DB-per-service, общение только по API).

## Ссылки в репо

- `../../CLAUDE.md` — operational правила для AI-агентов (компактнее).
- GitHub Issues — `github.com/PRO-Robotech/kacho-compute/issues` (долги, баги, planned). `TODO.md` упразднён.
- [07-known-divergences.md](07-known-divergences.md) — registry by-design расхождений с verbatim YC.
- `../../tests/newman/` — e2e regression suite (`cases/*.py` → `gen.py` → Postman-коллекции).
- Эталон-сервис (паттерны) — `../../../kacho-vpc/` (буквально смотри одноимённые файлы).
- Proto — `../../../kacho-proto/proto/kacho/cloud/compute/v1/`.
