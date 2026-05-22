---
level: functionality
repo: kacho-compute
anchors:
  - rpc: kacho.cloud.compute.v1.InstanceService/AddOneToOneNat
  - rpc: kacho.cloud.compute.v1.InstanceService/AttachDisk
  - rpc: kacho.cloud.compute.v1.InstanceService/AttachFilesystem
  - rpc: kacho.cloud.compute.v1.InstanceService/AttachNetworkInterface
  - rpc: kacho.cloud.compute.v1.InstanceService/Create
  - rpc: kacho.cloud.compute.v1.InstanceService/Delete
  - rpc: kacho.cloud.compute.v1.InstanceService/DetachDisk
  - rpc: kacho.cloud.compute.v1.InstanceService/DetachFilesystem
  - rpc: kacho.cloud.compute.v1.InstanceService/DetachNetworkInterface
  - rpc: kacho.cloud.compute.v1.InstanceService/Get
  - rpc: kacho.cloud.compute.v1.InstanceService/GetSerialPortOutput
  - rpc: kacho.cloud.compute.v1.InstanceService/List
  - rpc: kacho.cloud.compute.v1.InstanceService/ListAccessBindings
  - rpc: kacho.cloud.compute.v1.InstanceService/ListOperations
  - rpc: kacho.cloud.compute.v1.InstanceService/Move
  - rpc: kacho.cloud.compute.v1.InstanceService/Relocate
  - rpc: kacho.cloud.compute.v1.InstanceService/RemoveOneToOneNat
  - rpc: kacho.cloud.compute.v1.InstanceService/Restart
  - rpc: kacho.cloud.compute.v1.InstanceService/SetAccessBindings
  - rpc: kacho.cloud.compute.v1.InstanceService/SimulateMaintenanceEvent
  - rpc: kacho.cloud.compute.v1.InstanceService/Start
  - rpc: kacho.cloud.compute.v1.InstanceService/Stop
  - rpc: kacho.cloud.compute.v1.InstanceService/Update
  - rpc: kacho.cloud.compute.v1.InstanceService/UpdateAccessBindings
  - rpc: kacho.cloud.compute.v1.InstanceService/UpdateMetadata
  - rpc: kacho.cloud.compute.v1.InstanceService/UpdateNetworkInterface
status: implemented
source_sha: ""
---

# Instance lifecycle

CRUD виртуальных машин плюс полный набор lifecycle- и attach-операций.
`Instance` — центральный ресурс compute-домена: VM с набором ресурсов
(`memory` / `cores` / `core_fraction` / `gpus`), boot- и secondary-дисками,
network-интерфейсами, placement-политикой и метаданными.

## Зачем

Instance — единица потребления вычислительных ресурсов tenant'ом. Это самый
крупный L2-кластер сервиса (26 entry-points), потому что VM имеет богатый
жизненный цикл: помимо CRUD — переключение питания, горячий attach/detach
дисков/ФС/NIC, управление внешним NAT, перенос между проектами и зонами.

## Контракт

- `Get` / `List` / `ListOperations` / `ListAccessBindings` — sync read.
- `Create` / `Update` / `Delete` / `UpdateMetadata` — async (`Operation`).
- Power: `Start` / `Stop` / `Restart` — async переключение `Status`.
- Attach/detach: `AttachDisk` / `DetachDisk`, `AttachFilesystem` /
  `DetachFilesystem`, `AttachNetworkInterface` / `DetachNetworkInterface` —
  async; модифицируют состав ресурсов VM.
- NAT: `AddOneToOneNat` / `RemoveOneToOneNat` — async; внешний публичный IP
  через IPAM kacho-vpc.
- `UpdateNetworkInterface` — async смена subnet/SG/адресов интерфейса.
- `Move` (между проектами) / `Relocate` (между зонами) — async; atomic CAS.
- `SimulateMaintenanceEvent` — async; тест maintenance-policy.
- `GetSerialPortOutput` — sync read консольного вывода.
- `SetAccessBindings` / `UpdateAccessBindings` — async управление доступом.

## Lifecycle

`Status`: `PROVISIONING → RUNNING ⇄ STOPPING/STOPPED/STARTING/RESTARTING`,
плюс `UPDATING`, терминальный `DELETING`, аварийные `ERROR` / `CRASHED`.
`Create` валидирует `project_id` (kacho-iam) и NIC-spec (kacho-vpc) на
request-path, затем worker провиженит VM. Attach/detach дисков и NIC требуют
определённого `Status` (часть операций — только на `STOPPED`).

## Gotchas

- NIC внутри Instance — read-only denormalised mirror: источник истины —
  `NetworkInterface` (NIC) ресурс kacho-vpc, `nic_id` ссылается на него.
- Attach NIC к VM — atomic CAS на стороне kacho-vpc (инцидент NIC-attach
  race 2026-05-14, §Запрет #10); compute не делает software-refcheck.
- `metadata` опускается в ответе `List` (только в `Get`).
- Placement / `host_id` / `host_group_id` — связаны с internal-инвентарём
  `Hypervisor`; физика не раскрывается на публичной проекции (§«Инфра-данные»).
