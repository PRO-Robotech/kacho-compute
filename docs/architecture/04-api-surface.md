# 04 — API Surface

Полный список RPC kacho-compute + соответствующие REST endpoints (из
`google.api.http`-аннотаций в `kacho-proto/proto/kacho/cloud/compute/v1/`) и
Operation metadata/response (из `(kacho.cloud.api.operation)` options).

## Сводка

| Категория | Сервисов | RPC (примерно) | Listener | REST exposed |
|---|---:|---:|---|---|
| Public verbatim-YC мутируемые | 4 (`InstanceService`, `DiskService`, `ImageService`, `SnapshotService`) | ~60 | `:9090` public gRPC | ✅ через api-gateway (public mux), на обоих listener'ах |
| Public read-only справочники | 3 (`DiskTypeService`, `RegionService`, `ZoneService`) | 6 | `:9090` public gRPC | ✅ через api-gateway. Geography (Region/Zone) — owner kacho-compute (эпик `KAC-15`) |
| Operations | 1 (`OperationService`) | 2 (`Get`, `Cancel`) | `:9090` public gRPC | ✅ `/operations/{id}` (через api-gateway opsproxy) |
| Internal admin (kacho-only) | 3 (`InternalDiskTypeService`, `InternalRegionService`, `InternalZoneService`) | 9 | `:9091` internal gRPC | ✅ выборочно (`/compute/v1/diskTypes`, `/compute/v1/regions`, `/compute/v1/zones`) — только cluster-internal listener |
| Outbox stream | 1 (`InternalWatchService`) | 1 (`Watch`) | `:9091` internal gRPC | ❌ только server-to-server |

> ⚠️ REST-пути неоднородны (кальки YC API surface, proto-decided): top-level
> camelCase (`/compute/v1/diskTypes`, `/compute/v1/instances`), custom-методы
> с двоеточием (`:relocate`, `:serialPortOutput`, `:latestByFamily`,
> `:listAccessBindings`), child-list `.../operations`, action-методы через
> сегмент пути (`/updateMetadata`, `/addOneToOneNat`, `:attachDisk`),
> `OperationService.Get` — `/operations/{id}` (БЕЗ `/compute/v1/`-префикса).
> Нормализовать НЕЛЬЗЯ — сломает verbatim-YC. См. [`07-known-divergences.md`](07-known-divergences.md).

## DiskService (`disk_service.proto`, `:9090`)

| RPC | REST | sync/async | metadata / response | статус |
|---|---|---|---|---|
| `Get` | `GET /compute/v1/disks/{disk_id}` | sync | → `Disk` | ✅ |
| `List` | `GET /compute/v1/disks?folderId=&pageSize=&pageToken=&filter=&orderBy=` | sync | → `ListDisksResponse` | ✅ |
| `Create` | `POST /compute/v1/disks` body `*` | async | `CreateDiskMetadata{disk_id}` / `Disk` | ✅ (`kms_key_id`→`blocked:kacho-kms`, `snapshot_schedule_ids`→`blocked:kacho-snapshot-schedule`) |
| `Update` | `PATCH /compute/v1/disks/{disk_id}` body `*` | async | `UpdateDiskMetadata` / `Disk` | ✅ |
| `Delete` | `DELETE /compute/v1/disks/{disk_id}` | async | `DeleteDiskMetadata` / `google.protobuf.Empty` | ✅ |
| `ListOperations` | `GET /compute/v1/disks/{disk_id}/operations` | sync | → `ListDiskOperationsResponse` | ✅ |
| `Relocate` | `POST /compute/v1/disks/{disk_id}:relocate` body `*` | async | `RelocateDiskMetadata` / `Disk` | ⚠️ частично (cross-zone simplified) |
| `ListSnapshotSchedules` | (нет `google.api.http`) | sync | → `ListDiskSnapshotSchedulesResponse` | 🚫 `blocked:kacho-snapshot-schedule` |
| `ListAccessBindings` | `GET /compute/v1/disks/{resource_id}:listAccessBindings` | sync | → `access.ListAccessBindingsResponse` | ⏭️ no-op скелет |
| `SetAccessBindings` | `POST /compute/v1/disks/{resource_id}:setAccessBindings` body `*` | async | `access.SetAccessBindingsMetadata` / `access.AccessBindingsOperationResult` | ⏭️ no-op скелет |
| `UpdateAccessBindings` | `POST /compute/v1/disks/{resource_id}:updateAccessBindings` body `*` | async | `access.UpdateAccessBindingsMetadata` / `access.AccessBindingsOperationResult` | ⏭️ no-op скелет |

## ImageService (`image_service.proto`, `:9090`)

| RPC | REST | sync/async | metadata / response | статус |
|---|---|---|---|---|
| `Get` | `GET /compute/v1/images/{image_id}` | sync | → `Image` | ✅ |
| `GetLatestByFamily` | `GET /compute/v1/images:latestByFamily?folderId=&family=` | sync | → `Image` | ✅ |
| `List` | `GET /compute/v1/images?folderId=&...` | sync | → `ListImagesResponse` | ✅ |
| `Create` | `POST /compute/v1/images` body `*` | async | `CreateImageMetadata{image_id}` / `Image` | ✅ (`source` oneof: image_id/disk_id/snapshot_id/uri; `os_product_ids`→`blocked:kacho-marketplace`) |
| `Update` | `PATCH /compute/v1/images/{image_id}` body `*` | async | `UpdateImageMetadata` / `Image` | ✅ |
| `Delete` | `DELETE /compute/v1/images/{image_id}` | async | `DeleteImageMetadata` / `google.protobuf.Empty` | ✅ |
| `ListOperations` | `GET /compute/v1/images/{image_id}/operations` | sync | → `ListImageOperationsResponse` | ✅ |
| `ListAccessBindings` / `SetAccessBindings` / `UpdateAccessBindings` | `.../images/{resource_id}:listAccessBindings` / `:setAccessBindings` / `:updateAccessBindings` | sync / async / async | как у Disk | ⏭️ no-op скелет |

## SnapshotService (`snapshot_service.proto`, `:9090`)

| RPC | REST | sync/async | metadata / response | статус |
|---|---|---|---|---|
| `Get` | `GET /compute/v1/snapshots/{snapshot_id}` | sync | → `Snapshot` | ✅ |
| `List` | `GET /compute/v1/snapshots?folderId=&...` | sync | → `ListSnapshotsResponse` | ✅ |
| `Create` | `POST /compute/v1/snapshots` body `*` | async | `CreateSnapshotMetadata{snapshot_id, disk_id}` / `Snapshot` | ✅ (требует `disk_id`, disk READY) |
| `Update` | `PATCH /compute/v1/snapshots/{snapshot_id}` body `*` | async | `UpdateSnapshotMetadata` / `Snapshot` | ✅ |
| `Delete` | `DELETE /compute/v1/snapshots/{snapshot_id}` | async | `DeleteSnapshotMetadata` / `google.protobuf.Empty` | ✅ |
| `ListOperations` | `GET /compute/v1/snapshots/{snapshot_id}/operations` | sync | → `ListSnapshotOperationsResponse` | ✅ |
| `ListAccessBindings` / `SetAccessBindings` / `UpdateAccessBindings` | `.../snapshots/{resource_id}:listAccessBindings` / `:setAccessBindings` / `:updateAccessBindings` | sync / async / async | как у Disk | ⏭️ no-op скелет |

## InstanceService (`instance_service.proto`, `:9090`)

| RPC | REST | sync/async | metadata / response | статус |
|---|---|---|---|---|
| `Get` | `GET /compute/v1/instances/{instance_id}?view=` | sync | → `Instance` (FULL включает metadata) | ✅ |
| `List` | `GET /compute/v1/instances?folderId=&...` | sync | → `ListInstancesResponse` (metadata всегда омитится) | ✅ |
| `Create` | `POST /compute/v1/instances` body `*` | async | `CreateInstanceMetadata{instance_id}` / `Instance` | ✅ (`filesystem_specs[]`→`blocked:kacho-filesystem`. ⚠️ **без авто-NIC** — auto-NIC материализация `materializeNICs` удалена в `KAC-266`; инстанс создаётся без сетевых интерфейсов, NIC не создаётся/привязывается на Create; правильная сетевая модель — будущая переделка) |
| `Update` | `PATCH /compute/v1/instances/{instance_id}` body `*` | async | `UpdateInstanceMetadata` / `Instance` | ✅ (`resources_spec`/`platform_id` требуют STOPPED) |
| `Delete` | `DELETE /compute/v1/instances/{instance_id}` | async | `DeleteInstanceMetadata` / `google.protobuf.Empty` | ✅ (для каждого NIC с непустым `nic_id` — delete kacho-vpc `NetworkInterface`) |
| `UpdateMetadata` | `POST /compute/v1/instances/{instance_id}/updateMetadata` body `*` | async | `UpdateInstanceMetadataMetadata` / `Instance` | ✅ |
| `GetSerialPortOutput` | `GET /compute/v1/instances/{instance_id}:serialPortOutput?port=` | **sync** | → `GetInstanceSerialPortOutputResponse{contents}` | ✅ (синтетика, не операция) |
| `Stop` | `POST /compute/v1/instances/{instance_id}:stop` | async | `StopInstanceMetadata` / `google.protobuf.Empty` | ✅ |
| `Start` | `POST /compute/v1/instances/{instance_id}:start` | async | `StartInstanceMetadata` / `Instance` | ✅ |
| `Restart` | `POST /compute/v1/instances/{instance_id}:restart` | async | `RestartInstanceMetadata` / `google.protobuf.Empty` | ✅ |
| `AttachDisk` | `POST /compute/v1/instances/{instance_id}:attachDisk` body `*` | async | `AttachInstanceDiskMetadata{instance_id, disk_id}` / `Instance` | ✅ |
| `DetachDisk` | `POST /compute/v1/instances/{instance_id}:detachDisk` body `*` | async | `DetachInstanceDiskMetadata` / `Instance` | ✅ |
| `AttachFilesystem` | `POST /compute/v1/instances/{instance_id}:attachFilesystem` body `*` | async | `AttachInstanceFilesystemMetadata` / `Instance` | 🚫 `blocked:kacho-filesystem` |
| `DetachFilesystem` | `POST /compute/v1/instances/{instance_id}:detachFilesystem` body `*` | async | `DetachInstanceFilesystemMetadata` / `Instance` | 🚫 `blocked:kacho-filesystem` |
| `AttachNetworkInterface` | `POST /compute/v1/instances/{instance_id}:attachNetworkInterface` body `*` | async | `AttachInstanceNetworkInterfaceMetadata` / `Instance` | ✅ (требует STOPPED) |
| `DetachNetworkInterface` | `POST /compute/v1/instances/{instance_id}:detachNetworkInterface` body `*` | async | `DetachInstanceNetworkInterfaceMetadata` / `Instance` | ✅ (требует STOPPED) |
| `AddOneToOneNat` | `POST /compute/v1/instances/{instance_id}/addOneToOneNat` body `*` | async | `AddInstanceOneToOneNatMetadata` / `Instance` | ✅ |
| `RemoveOneToOneNat` | `POST /compute/v1/instances/{instance_id}/removeOneToOneNat` body `*` | async | `RemoveInstanceOneToOneNatMetadata` / `Instance` | ✅ |
| `UpdateNetworkInterface` | `PATCH /compute/v1/instances/{instance_id}/updateNetworkInterface` body `*` | async | `UpdateInstanceNetworkInterfaceMetadata` / `Instance` | ✅ (OCC через xmin) |
| `ListOperations` | `GET /compute/v1/instances/{instance_id}/operations` | sync | → `ListInstanceOperationsResponse` | ✅ |
| `Relocate` | `POST /compute/v1/instances/{instance_id}:relocate` body `*` | async | `RelocateInstanceMetadata` / `Instance` | 🚫 blocked (cross-zone disk move) |
| `SimulateMaintenanceEvent` | `POST /compute/v1/instances/{instance_id}:simulateMaintenanceEvent` body `*` | async | `SimulateInstanceMaintenanceEventMetadata` / `google.protobuf.Empty` | ⏭️ no-op |
| `ListAccessBindings` / `SetAccessBindings` / `UpdateAccessBindings` | `.../instances/{resource_id}:listAccessBindings` / `:setAccessBindings` / `:updateAccessBindings` | sync / async / async | как у Disk | ⏭️ no-op скелет |

(`GuestStopInstanceMetadata` / `PreemptInstanceMetadata` / `CrashInstanceMetadata`
— metadata-сообщения без RPC; зарезервированы для будущих guest-инициированных
переходов, в Kachō не эмитятся.)

## DiskTypeService (`disk_type_service.proto`, `:9090`)

| RPC | REST | sync/async | response | статус |
|---|---|---|---|---|
| `Get` | `GET /compute/v1/diskTypes/{disk_type_id}` | sync | → `DiskType` | ✅ |
| `List` | `GET /compute/v1/diskTypes?pageSize=&pageToken=` | sync | → `ListDiskTypesResponse` | ✅ |

## RegionService (`region_service.proto`, `:9090`) — Geography, owner kacho-compute (эпик KAC-15)

| RPC | REST | sync/async | response | статус |
|---|---|---|---|---|
| `Get` | `GET /compute/v1/regions/{region_id}` | sync | → `Region` | ✅ |
| `List` | `GET /compute/v1/regions?pageSize=&pageToken=` | sync | → `ListRegionsResponse` | ✅ |

## ZoneService (`zone_service.proto`, `:9090`) — Geography, owner kacho-compute (эпик KAC-15)

| RPC | REST | sync/async | response | статус |
|---|---|---|---|---|
| `Get` | `GET /compute/v1/zones/{zone_id}` | sync | → `Zone` | ✅ |
| `List` | `GET /compute/v1/zones?pageSize=&pageToken=` | sync | → `ListZonesResponse` | ✅ |

> Geography (Region/Zone) перенесена в kacho-compute из kacho-vpc (эпик `KAC-15`):
> compute читает зоны/регионы из своих таблиц (нет proxy / `skipPeer`-fallback);
> другие сервисы (kacho-vpc) валидируют `zone_id` вызовом `ZoneService.Get`.

## OperationService (`:9090`)

| RPC | REST | sync/async | response | примечание |
|---|---|---|---|---|
| `Get` | `GET /operations/{operation_id}` (БЕЗ `/compute/v1/`) | sync | → `operation.Operation` | api-gateway opsproxy маршрутизирует по первым 3 символам id (`epd...` → kacho-compute). prefix `epd` для всех compute-операций |
| `Cancel` | `POST /operations/{operation_id}:cancel` | sync | → `operation.Operation` | best-effort cancel; в control-plane операции быстрые → обычно уже `done` |

## Internal сервисы (`:9091`, НЕ на external TLS endpoint)

### InternalDiskTypeService (`internal_catalog_service.proto`) — kacho-only

| RPC | REST (api-gateway internal mux) | response | примечание |
|---|---|---|---|
| `Create` | `POST /compute/v1/diskTypes` body `{id, description, zone_ids}` | → `DiskType` | admin задаёт `id` явно (PK, immutable) |
| `Update` | `PATCH /compute/v1/diskTypes/{disk_type_id}` body `{description, zone_ids}` | → `DiskType` | |
| `Delete` | `DELETE /compute/v1/diskTypes/{disk_type_id}` | → `DeleteDiskTypeResponse{}` | |

### InternalRegionService (`internal_catalog_service.proto`) — kacho-only (эпик KAC-15)

| RPC | REST (api-gateway internal mux) | response | примечание |
|---|---|---|---|
| `Create` | `POST /compute/v1/regions` body `{id, name}` | → `Region` | admin задаёт `id` явно (PK, immutable) |
| `Update` | `PATCH /compute/v1/regions/{region_id}` body `{name}` | → `Region` | |
| `Delete` | `DELETE /compute/v1/regions/{region_id}` | → `DeleteRegionResponse{}` | блокируется (FK `zones.region_id` RESTRICT) если есть зоны |

### InternalZoneService (`internal_catalog_service.proto`) — kacho-only

| RPC | REST (api-gateway internal mux) | response | примечание |
|---|---|---|---|
| `Create` | `POST /compute/v1/zones` body `{id, region_id, name, status}` | → `Zone` | admin задаёт `id` явно (PK, immutable) |
| `Update` | `PATCH /compute/v1/zones/{zone_id}` body `{region_id, name, status}` | → `Zone` | |
| `Delete` | `DELETE /compute/v1/zones/{zone_id}` | → `DeleteZoneResponse{}` | проверяет своих dependents (instances/disks/disk_types); кросс-сервисных (vpc-подсети) НЕ проверяет — admin-ответственность |

> ⚠️ `InternalDiskTypeService` / `InternalZoneService` — kacho-only расширение;
> в verbatim YC Compute API есть только `DiskTypeService.{Get,List}` /
> `ZoneService.{Get,List}` (статический discovery, без CRUD). Эти admin-RPC
> зарегистрированы в `kacho-api-gateway/internal/restmux/mux.go` под блоком
> `computeInternalAddr` → попадают **только** на cluster-internal listener,
> **не** на external TLS endpoint (`api.kacho.local:443`, advertised для `yc`
> CLI). Любой новый admin-RPC, которого нет в verbatim-YC — добавлять ТОЛЬКО в
> `Internal*` сервис на `:9091` (workspace `CLAUDE.md` §запрет 6). См.
> [`06-conventions.md`](06-conventions.md#admin-boundary).

### InternalWatchService (`internal_watch_service.proto`) — server-to-server

| RPC | REST | примечание |
|---|---|---|
| `Watch` | ❌ нет (gRPC server-stream только) | `Watch(WatchRequest{kinds[]?, from_sequence_no})` → `stream Event{sequence_no, resource_kind, resource_id, event_type, payload(Struct), created_at}`. outbox stream через LISTEN/NOTIFY. Не зарегистрирован в api-gateway restmux. См. [`02-data-flows.md`](02-data-flows.md#8-outbox--listennotify--internalwatchservice) |

## Operations (LRO) — общая модель

Все мутации (`Create/Update/Delete/Start/Stop/Restart/Relocate/AttachDisk/
DetachDisk/AddOneToOneNat/RemoveOneToOneNat/UpdateNetworkInterface/UpdateMetadata/
SimulateMaintenanceEvent/Set|UpdateAccessBindings`) возвращают
`operation.Operation`. Клиент полит `OperationService.Get(operation_id)` до
`done=true` (REST: `GET /operations/{id}`, БЕЗ `/compute/v1/`). api-gateway имеет
in-process `opsproxy` — один URL `/operations/{id}` маршрутизируется по 3-char
prefix id на нужный backend (`epd...` → kacho-compute; `enp...`/`e9b...` →
kacho-vpc; `b1g...` → kacho-iam — Account/Project, заменил resource-manager в
KAC-124). `PrefixOperationCompute == PrefixInstance
== "epd"`. Неизвестный prefix → `400 INVALID_ARGUMENT "operation_id has unknown
prefix"` (intentional fail-fast — частично расходится с YC, как и у VPC; общий
issue в `kacho-api-gateway`). `response` для Delete/Stop/Restart/
SimulateMaintenanceEvent = `google.protobuf.Empty`; metadata всегда заполнено
(тип `DeleteXxxMetadata` / etc.) и доступно с момента создания операции.

## Где смотреть proto

```
kacho-proto/proto/kacho/cloud/compute/v1/
├── disk.proto / disk_service.proto
├── image.proto / image_service.proto
├── snapshot.proto / snapshot_service.proto
├── instance.proto / instance_service.proto
├── disk_type.proto / disk_type_service.proto
├── region.proto / region_service.proto  Geography (owner kacho-compute, эпик KAC-15)
├── zone.proto / zone_service.proto       Geography (owner kacho-compute, эпик KAC-15)
├── hardware_generation.proto / kek.proto / maintenance.proto / application.proto / package_options.proto
│
├── internal_watch_service.proto         InternalWatchService.Watch (outbox stream)
├── internal_catalog_service.proto       InternalDiskTypeService / InternalRegionService / InternalZoneService (admin CRUD)
│
└── (vendored, реализация отложена) disk_placement_group*.proto, placement_group*.proto,
    host_group*.proto, host_type*.proto, gpu_cluster*.proto, filesystem*.proto,
    snapshot_schedule*.proto, reserved_instance_pool*.proto, maintenance_service.proto,
    instancegroup/*.proto
```

Generated stubs: `kacho-proto/gen/go/kacho/cloud/compute/v1/...`. Импорт:

```go
computev1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/compute/v1"
```
