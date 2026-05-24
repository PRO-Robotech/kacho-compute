---
level: functionality
repo: kacho-compute
anchors:
  - rpc: kacho.cloud.operation.OperationService/Cancel
  - rpc: kacho.cloud.operation.OperationService/Get
status: implemented
source_sha: ""
---

# Operations

Поллинг long-running async-операций. Общий `OperationService` — точка, где
клиент дожидается завершения любой мутации kacho-compute.

## Зачем

Все мутирующие RPC kacho-compute (`Create` / `Update` / `Delete` /
`Start` / `Stop` / `Restart` / `Attach*` / `Detach*` / `Move` / `Relocate`
…) возвращают не ресурс, а `operation.Operation` (§Запрет #9 — синхронный
возврат ресурса из мутации запрещён). Клиент поллит `OperationService.Get(id)`
до `done=true` и забирает `response` или `error` из `oneof result`.

## Контракт

- `Get` — sync; возвращает `Operation` по id: `done`, `metadata` (Any —
  например `CreateInstanceMetadata{instance_id}`), `oneof result`
  (`response` Any | `error` google.rpc.Status).
- `Cancel` — sync; запрос на отмену ещё не завершённой операции.

## Lifecycle

- Мутация создаёт `Operation`-row (`done=false`) в общей `operations`-таблице
  (из `kacho-corelib/operations`).
- Worker исполняет операцию (provisioning VM, создание диска, attach NIC …)
  и переводит `done=false → true`, заполняя `response` либо `error`.
- Клиент поллит `Get`; `Cancel` запрашивает прерывание (best-effort,
  зависит от стадии операции).

## Gotchas

- Operations — per-service: kacho-compute ведёт свою `operations`-таблицу,
  это не кросс-доменный ресурс.
- `OperationService` — реализация из corelib, общая для всех kacho-сервисов;
  пакет `kacho.cloud.operation` (не `compute.v1`).
- Per-resource истории (`InstanceService.ListOperations`,
  `DiskService.ListOperations` и т.п.) — отдельные RPC своих L2-кластеров;
  здесь только generic `Get` / `Cancel`.

## Связанные заметки

<!-- archgraph:links -->
- ↑ Приклад: [[_l1-kacho-compute]]
- ↓ Функции (call-дерево, RPC contract): [[l3-l2-operations]]
- Переменные: [[l4-kacho-compute]]
<!-- /archgraph:links -->
