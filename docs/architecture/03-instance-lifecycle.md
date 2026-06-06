# 03 — Instance Lifecycle (state-машина)

`kacho-compute` — control-plane only: реального гипервизора нет, переходы
`Instance.Status` детерминированы и происходят **внутри worker'а
соответствующей операции** (worker «имитирует» короткую асинхронную работу:
меняет status → конечное состояние синхронно в той же TX, без таймеров,
без heartbeat'ов из VM). Это by-design расхождение с реальным YC (где переходы
занимают реальное время и `Instance.status` проходит промежуточные состояния
типа `STARTING`/`STOPPING`/`PROVISIONING`) — см.
[`07-known-divergences.md`](07-known-divergences.md) §5.

## `Instance.Status` enum

Из `instance.proto`, `Instance.Status` (verbatim YC):

| # | Значение | Семантика |
|---|---|---|
| 0 | `STATUS_UNSPECIFIED` | не используется (не должно встречаться у живого Instance) |
| 1 | `PROVISIONING` | ждёт аллокации ресурсов (промежуточный — в Kachō мелькает только внутри TX Create) |
| 2 | `RUNNING` | работает нормально (стабильное состояние) |
| 3 | `STOPPING` | останавливается (промежуточный — в Kachō мелькает внутри TX Stop) |
| 4 | `STOPPED` | остановлен (стабильное состояние) |
| 5 | `STARTING` | запускается (промежуточный — внутри TX Start) |
| 6 | `RESTARTING` | перезапускается (промежуточный — внутри TX Restart) |
| 7 | `UPDATING` | обновляется (промежуточный — внутри TX Update resources_spec/platform_id) |
| 8 | `ERROR` | проблема, не операбелен (в control-plane не достигается штатно — зарезервировано) |
| 9 | `CRASHED` | упал, будет авто-перезапущен (в control-plane не достигается штатно) |
| 10 | `DELETING` | удаляется (промежуточный — внутри TX Delete) |

В БД (`instances.status` TEXT) хранится строковое имя enum-значения; `created_at`
default → `PROVISIONING`, после успешного Create — `RUNNING`. `status_message`
поле — **всегда пусто** (в control-plane не используется).

## Transition table (RPC × precondition × end-status × Operation.response)

«Стабильные» состояния — `RUNNING` и `STOPPED`. Все мутации проверяют
precondition по текущему status; нарушение → `FailedPrecondition`
(grpc `FAILED_PRECONDITION`). Промежуточные статусы (`PROVISIONING`/`STARTING`/
`STOPPING`/`RESTARTING`/`UPDATING`/`DELETING`) в стационаре не видны — они только
внутри TX worker'а.

| RPC | preconditions (иначе `FailedPrecondition`) | промежуточный status (внутри TX) | end status | Operation.response |
|---|---|---|---|---|
| `Create` | — (создаётся **без авто-NIC** — см. ниже «Instance.Create без авто-NIC») | `PROVISIONING` | `RUNNING` | `Instance` |
| `Start` | `status ∈ {STOPPED}` | `STARTING` | `RUNNING` | `Instance` |
| `Stop` | `status ∈ {RUNNING}` | `STOPPING` | `STOPPED` | `google.protobuf.Empty` |
| `Restart` | `status ∈ {RUNNING}` | `RESTARTING` | `RUNNING` | `google.protobuf.Empty` |
| `Update` (изменяет `resources_spec` / `platform_id`) | `status ∈ {STOPPED}` | `UPDATING` | `STOPPED` | `Instance` |
| `Update` (изменяет только `name`/`labels`/`description`/`service_account_id`/`network_settings`/`placement_policy`/`scheduling_policy`/`maintenance_policy`/`serial_port_settings`) | any | — | unchanged | `Instance` |
| `UpdateMetadata` | any | — | unchanged | `Instance` |
| `AttachDisk` | `status ∈ {RUNNING, STOPPED}`; disk `READY` & same zone & not attached | — | unchanged | `Instance` |
| `DetachDisk` | `status ∈ {RUNNING, STOPPED}`; disk attached & **not boot** | — | unchanged | `Instance` |
| `AddOneToOneNat` | `status ∈ {RUNNING, STOPPED}`; NIC index valid; NAT ещё не задан | — | unchanged | `Instance` |
| `RemoveOneToOneNat` | `status ∈ {RUNNING, STOPPED}`; NIC index valid; NAT задан | — | unchanged | `Instance` |
| `UpdateNetworkInterface` | NIC index valid; OCC через `xmin` (read-modify-write); precondition-семантика probe YC (вероятно `STOPPED` для смены subnet) | — | unchanged | `Instance` |
| `AttachNetworkInterface` | `status ∈ {STOPPED}` (proto-комментарий verbatim YC); NIC index ещё не занят | — | unchanged | `Instance` |
| `DetachNetworkInterface` | `status ∈ {STOPPED}` (proto-комментарий); NIC index valid; нельзя detach последний NIC | — | unchanged | `Instance` |
| `GetSerialPortOutput` | any (это **sync** read, не операция) | — | — | `GetInstanceSerialPortOutputResponse{contents}` (синтетический текст) |
| `Relocate` | `status ∈ {STOPPED}` или RUNNING-с-restart (proto: «running instance will be restarted») — **blocked** (нужен cross-zone disk move) | — | (blocked) | `Instance` |
| `SimulateMaintenanceEvent` | any — **no-op** | — | unchanged | `google.protobuf.Empty` |
| `Delete` | any (Instance с дисками — отвязывает по `auto_delete`; для каждого NIC с непустым `nic_id` — delete kacho-vpc `NetworkInterface`) | `DELETING` | (deleted) | `google.protobuf.Empty` |

⚠️ verbatim YC-тексты precondition-ошибок — **probe реального YC** при написании
acceptance/newman. До probe — placeholder-формулировки (зафиксированы в
[`07-known-divergences.md`](07-known-divergences.md) §3); примеры ожидаемых:
`"Instance must be stopped"`, `"Instance is not running"`, `"Instance is already
running"`, `"Cannot stop instance in state STOPPED"`, `"The disk is being used"`.

## Диаграмма переходов

```mermaid
stateDiagram-v2
  [*] --> PROVISIONING: Create (внутри TX)
  PROVISIONING --> RUNNING: Create завершён

  RUNNING --> STOPPING: Stop (внутри TX)
  STOPPING --> STOPPED: Stop завершён

  STOPPED --> STARTING: Start (внутри TX)
  STARTING --> RUNNING: Start завершён

  RUNNING --> RESTARTING: Restart (внутри TX)
  RESTARTING --> RUNNING: Restart завершён

  STOPPED --> UPDATING: Update resources_spec/platform_id (внутри TX)
  UPDATING --> STOPPED: Update завершён

  RUNNING --> DELETING: Delete (внутри TX)
  STOPPED --> DELETING: Delete (внутри TX)
  DELETING --> [*]: Delete завершён

  note right of RUNNING
    Stable. Допускают AttachDisk/DetachDisk/AddNat/RemoveNat
    (статус не меняется); name/labels Update; UpdateMetadata.
  end note
  note right of STOPPED
    Stable. Допускают то же + Update resources_spec/platform_id,
    AttachNetworkInterface/DetachNetworkInterface, Relocate(blocked).
  end note
  note left of ERROR
    ERROR / CRASHED — зарезервированы; в control-plane
    не достигаются штатно (нет реального гипервизора).
  end note
```

## Control-plane simulation note

- Нет реального гипервизора — переходы статусов **мгновенны** (внутри TX worker'а),
  без задержки provisioning. Реальный YC: provisioning занимает секунды-минуты,
  `Instance.status` реально проходит `STARTING`/`STOPPING`/etc.
- `GetSerialPortOutput` возвращает **синтетический** текст (стабильный per-instance
  плейсхолдер вида `[ OK ] Reached target Multi-User System.` и т.п.), не реальный
  console-вывод.
- `boot_disk` `disk_data` не существует; `Image.Create` через `uri`-source —
  «download» мгновенный, `storage_size` синтетический.
- `SimulateMaintenanceEvent` — no-op: operation сразу `done`, status не меняется
  (реальный YC переселил бы Instance согласно `maintenance_policy`).
- При краше pod'а compute операция остаётся `done=false` навсегда (известное
  ограничение `operations.Run` без heartbeat/cleanup — общий для всех kacho-*
  сервисов; `operations.Wait(30s)` на graceful shutdown спасает только от
  in-flight worker'ов при штатном завершении).

## AttachDisk / DetachDisk инварианты

- `AttachDisk`: disk должен (1) существовать → иначе `NotFound "Disk <X> not
  found"`; (2) быть `READY` → иначе `FailedPrecondition`; (3) быть в той же
  зоне, что instance (`disks.zone_id == instances.zone_id`) → иначе
  `InvalidArgument`/`FailedPrecondition`; (4) не быть уже attached к какому-либо
  Instance (строки в `attached_disks` по `disk_id`) → иначе `FailedPrecondition
  "The disk is being used"` (verbatim — text probe). `device_name` (если задан)
  уникален в пределах instance (`attached_disks_device_uniq`).
- `DetachDisk`: указывается `disk_id` **или** `device_name` (proto
  `(exactly_one)`); строка должна существовать → иначе `NotFound`; disk не
  должен быть boot (`is_boot=false`) → иначе `FailedPrecondition` (boot disk
  можно убрать только удалив Instance). При detach строка `attached_disks`
  удаляется (disk сам остаётся, его `auto_delete` тут не играет — он играет
  только в `Instance.Delete`).
- Ровно один boot disk на instance (`attached_disks_boot_uniq` partial UNIQUE на
  `instance_id WHERE is_boot`). `boot_disk` ресурса = эта строка.

## AddOneToOneNat / RemoveOneToOneNat / UpdateNetworkInterface инварианты

- `AddOneToOneNat(network_interface_index, internal_address?, one_to_one_nat_spec?)`:
  precondition `status ∈ {RUNNING, STOPPED}`; NIC с таким `index` существует;
  NAT ещё не задан. Если `one_to_one_nat_spec.address` указан — existence-check
  external Address через `vpcClient.GetAddress`; если не указан — в control-plane
  присваивается синтетический external IP. Записывает `primary_v4_nat` JSONB +
  outbox `Instance UPDATED`.
- `RemoveOneToOneNat(network_interface_index, internal_address?)`: precondition
  как у Add; NAT должен быть задан; обнуляет `primary_v4_nat`; освобождает
  external Address best-effort через `vpcClient` (не валит операцию, если VPC
  недоступен).
- `UpdateNetworkInterface(network_interface_index, update_mask, subnet_id?,
  primary_v4_address_spec?, primary_v6_address_spec?, security_group_ids?)`:
  read-modify-write с OCC через `xmin::text` (как `SecurityGroup.UpdateRules` в
  VPC). `update_mask` discipline та же, что у других Update RPC (unknown поле →
  `InvalidArgument`; пустой mask → full-PATCH мутабельных полей). Смена `subnet_id`
  / `security_group_ids` валидируется через `vpcClient`. Precondition-семантика
  (требует ли `STOPPED`) — **probe YC** (зафиксировано в `07-known-divergences.md`).
- `AttachNetworkInterface` / `DetachNetworkInterface`: proto-комментарии явно
  требуют `status == STOPPED`; нельзя detach последний NIC (минимум 1 на Instance).

## Instance.Create без авто-NIC (`materializeNICs` удалён, KAC-266)

⚠️ **`Instance.Create` больше не создаёт и не привязывает NIC автоматически.**
Авто-материализация сетевых интерфейсов (`materializeNICs`) удалена в `KAC-266`:
worker `Instance.Create` больше **не** создаёт kacho-vpc `Address` /
`NetworkInterface` и не вызывает attach. Инстанс создаётся **без сетевых
интерфейсов** (`instance_network_interfaces` остаётся пустой). Правильная сетевая
модель (явная привязка NIC к инстансу) — **будущая переделка** (вне scope KAC-266;
завести отдельный эпик при возврате к сетевой модели).

Контекст (эпик `KAC-9`, для истории): Compute-NIC задумывался как backed
kacho-vpc-ресурсом `NetworkInterface` (source of truth для адреса/SG/data-plane);
id бэкующего NIC — `compute.v1.NetworkInterface.nic_id` (proto field 7), колонка
`instance_network_interfaces.nic_id` (миграция `0005_instance_nic_id.sql`)
сохраняется в схеме, но при `Instance.Create` больше не заполняется. NIC-RPC
`AttachToInstance` / `DetachFromInstance` на стороне kacho-vpc также сняты в `KAC-266`.

- **`Instance.Delete` (worker):** для каждого NIC с непустым `nic_id` (если такие
  есть от прежних данных) — delete kacho-vpc `NetworkInterface` (что освобождает
  его `Address`-ресурсы). Best-effort: VPC недоступен → log warning, операцию не
  валим (как и с one_to_one_nat addresses).

Runtime cross-domain edge `kacho-compute → kacho-vpc` сохраняется (валидация
ссылок + delete NIC при Delete + эфемерный Address IPAM), но **без** create/attach
NIC на пути `Instance.Create`.

## `Update` resources_spec / platform_id требует STOPPED

`UpdateInstanceRequest` с `resources_spec` или `platform_id` в `update_mask`
(или в теле при пустом mask) → если `status != STOPPED` → `FailedPrecondition
"Instance must be stopped"` (verbatim — text probe). Изменение остальных
mutable-полей (`name`/`description`/`labels`/`service_account_id`/
`network_settings`/`placement_policy`/`scheduling_policy`/`maintenance_policy`/
`maintenance_grace_period`/`serial_port_settings`) допустимо в любом статусе.
`metadata` — только через `UpdateMetadata` RPC (не через `Update`), в любом
статусе. immutable: `zone_id` (меняется через `Relocate` — blocked), `boot_disk`
(меняется через `AttachDisk` с прежним detach — но boot detach запрещён, фактически
boot disk фиксирован на время жизни Instance).
