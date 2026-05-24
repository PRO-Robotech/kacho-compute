---
level: functionality
repo: kacho-compute
anchors:
  - rpc: kacho.cloud.compute.v1.InternalWatchService/Watch
status: implemented
source_sha: ""
---

# Watch

Поток событий изменения compute-ресурсов. `InternalWatchService.Watch` —
internal server-streaming RPC: отдаёт упорядоченный поток `Event` из таблицы
`compute_outbox` начиная с указанной позиции.

## Зачем

Watch — механизм доставки изменений ресурсов (`Instance` / `Disk` / `Image` /
`Snapshot`) внешним consumer'ам: observability, admin-tooling, будущие
подписчики. Транзакционный outbox гарантирует, что событие записывается в той
же транзакции, что и мутация ресурса — никаких потерянных или фантомных
изменений.

## Контракт

- `Watch` (internal, 9091): server-stream `Event`. `WatchRequest` задаёт
  `kinds` (фильтр по типу ресурса, пусто = все) и `from_sequence_no`
  (возобновление с позиции; `0` = только новые события).
- `Event`: `sequence_no` (монотонный BIGSERIAL), `resource_kind`,
  `resource_id`, `event_type` (`CREATED` / `UPDATED` / `DELETED`),
  `payload` (JSON-состояние ресурса или tombstone), `created_at`.
- Метод помечен `permission = "<exempt>"` — internal, вне authz-gate.

## Lifecycle

Мутация ресурса в worker'е пишет строку в `compute_outbox` в той же
транзакции. Сервер `Watch` делает `SELECT compute_outbox WHERE sequence_no >
cursor ORDER BY sequence_no` + PG `LISTEN/NOTIFY` для real-time wake-up,
стримит события по мере появления.

## Gotchas

- `Watch` — **только** internal-listener (9091): не публикуется через
  api-gateway на external TLS (§Запрет #6) — поток несёт полное состояние
  ресурсов, включая чувствительные поля.
- `from_sequence_no` exclusive — consumer хранит последний обработанный
  `sequence_no` как курсор для resume после переподключения.
- Watch RPC compute-домена сохранён (в отличие от удалённого Watch в
  kacho-vpc 1.0) — здесь это internal outbox-stream, не tenant-facing Watch.
- `payload` — `google.protobuf.Struct` (JSON-репрезентация domain-объекта),
  не строго типизированное сообщение.

## Связанные заметки

<!-- archgraph:links -->
- ↑ Приклад: [[_l1-kacho-compute]]
- ↓ Функции (call-дерево, RPC contract): [[l3-l2-watch]]
- Переменные: [[l4-kacho-compute]]
<!-- /archgraph:links -->
