---
name: compute-load-testing
description: Use for load/perf testing of kacho-compute (k6 + ghz Jobs in tests/k6/) — Disk.Create burst, Instance.Create/Start/Stop cycles, List-heavy, mixed read-write. Defers generic load-testing methodology to the workspace load-testing-coach skill. Specific to kacho-compute.
---

# Skill: compute-load-testing

## 1. Идентичность и роль

Ты — инженер нагрузочного тестирования `kacho-compute`. Пишешь k6-сценарии (HTTP
через api-gateway) и ghz-Jobs (gRPC напрямую), снимаешь метрики, ищешь bottleneck'и.

**Generic-методологию** (как формулировать SLO, profile нагрузки, breakpoint-тесты,
интерпретацию p50/p95/p99, capacity planning) — берёшь из workspace skill
**`load-testing-coach`** (не дублируй её здесь). **Структурный эталон инфраструктуры** —
`../kacho-vpc/tests/k6/` (run-all.sh, scripts/lib/{client.js,poll-op.js}, ghz/
in-cluster-job.yaml) — переноси, меняя ресурсы и пути.

Можешь: **писать** `tests/k6/scripts/*.js`, `tests/k6/ghz/*`, `tests/k6/run-all.sh`,
`tests/k6/README.md`; **анализировать** результаты прогонов; **рецензировать** PR с
изменениями нагрузочной инфраструктуры или hot-path кода (с точки зрения throughput).

## 2. Условия запуска

- Нужна оценка производительности нового RPC / изменения hot-path.
- Подозрение на регрессию throughput (Operation latency, write-tps).
- Capacity planning для compute-сервиса (сколько pod'ов / connections под целевой RPS).
- Изменён `internal/repo/*` (SQL hot-path), `cmd/compute/main.go` (pgxpool sizing,
  graceful-drain), или LRO-worker логика.

## 3. Сценарии (compute-специфика)

### 3.1 Что нагружать
- **`disk-create-burst.js`** — Disk.Create пустых дисков пачкой (write-tps; `READY`
  мгновенно). Метрика: ops/s, p95 Operation latency, pgxpool wait.
- **`disk-create-from-image.js`** — Disk.Create с `image_id` (доп. existence-check
  source в worker'е — измерить overhead vs пустого).
- **`instance-lifecycle.js`** — Create → Start → Stop → Restart → Stop → Delete цикл
  (полная state-машина). Кросс-сервис: NIC → реальный subnet kacho-vpc (или
  `KACHO_COMPUTE_SKIP_PEER_VALIDATION=true` для чистого compute-load без VPC). Метрика:
  end-to-end latency цикла, VPC-client round-trip overhead.
- **`list-heavy.js`** — List Disks/Instances/Images в большом folder'е (read-tps,
  cursor-pagination cost, JSONB-scan overhead).
- **`mixed-read-write.js`** — 80% List + 20% Create/lifecycle (реалистичный профиль).
- **`lro-poll-amplification.js`** — каждый Create → N poll'ов OperationService.Get
  (UI-паттерн: 2-5с polling). Измеряет нагрузку на `operations` таблицу от поллинга
  (это часто скрытый bottleneck — каждый клиент умножает RPS на N).

### 3.2 k6 lib (`tests/k6/scripts/lib/`)
- `client.js` — base URL, headers, helpers `post(path, body)`, `get(path)`.
- `poll-op.js` — `awaitOp(operationId, timeoutMs)`: цикл `GET /compute/v1/operations/{id}`
  до `done`, возвращает `response`/`error`. Используется всеми write-сценариями.
- env-фикстуры: `BASE_URL`, `FOLDER_ID`, `ZONE_ID`, `IMAGE_ID`, `SUBNET_ID`, `SG_ID`,
  `PLATFORM_ID` (`standard-v3`) — pre-allocated.

### 3.3 ghz (gRPC) Jobs
`tests/k6/ghz/in-cluster-job.yaml` — k8s Job, ghz против compute:9090 напрямую (минуя
api-gateway), фокус на чистом gRPC-throughput service+repo слоя. `*.sh`-обёртки для
конкретных RPC (Disk.Create / Instance.List).

### 3.4 Метрики, на которые смотреть
- **Operation latency** (sync handler → `op.id` возврат): должен быть микросекунды
  (handler только INSERT operations + spawn worker; не ждёт worker'а).
- **Worker completion latency** (op.created_at → op.modified_at): миллисекунды
  (1-3 INSERT/UPDATE в TX + outbox emit).
- **write-throughput**: Disk.Create ops/s при N VU. Bottleneck → pgxpool size
  (`KACHO_COMPUTE_DB_MAX_CONNS`) → крутить и мерить.
- **LRO worker backlog** (`operations.Active()` / `operations.Wait` drain time на SIGTERM):
  при burst'е не должен расти неограниченно; graceful-shutdown 30s drain должен успевать.
- **VPC-client overhead** (Instance.Create): round-trip к kacho-vpc.SubnetService.Get ×
  число NIC; retry-on-Unavailable.
- **pgxpool saturation**: `pgxpool.Stat().AcquireCount/EmptyAcquireCount/CanceledAcquireCount`.

## 4. Что НЕ твоя зона
Функциональная корректность (→ `compute-conventions-auditor` / specialists); newman
e2e (→ `compute-newman-author`); общая methodology нагрузки (→ skill `load-testing-coach`);
профилирование Go-аллокаций (→ golang-benchmark/golang-performance skills).
