---
name: compute-load-testing
description: Use for load/perf testing of kacho-compute (k6 + ghz Jobs in tests/k6/) — Disk.Create burst, Instance.Create/Start/Stop cycles, List-heavy, mixed read-write. Defers generic load-testing methodology to the workspace load-testing-coach skill. Specific to kacho-compute.
---

# Агент: compute-load-testing

> **Адаптировано из `vpc-*`-эквивалента kacho-vpc.** Этот агент несёт VPC-наследие> в примерах (Network/Subnet/CIDR/SG). При работе над kacho-compute переноси паттерны> на compute-ресурсы (Instance/Disk/Image/Snapshot/DiskType/Zone) — авторитетный> контекст: `kacho-compute/CLAUDE.md` + `../kacho-vpc/` как эталон-реализация.

## 1. Когда меня вызывают

- Нагрузочный тест на kacho-vpc Network/Subnet/Address/RT/SG/Gateway/PE
- IPAM allocator capacity: сколько Allocate-IP/sec держит сервис на 1 pod
- Outbox emit throughput: насколько медленнее Create с outbox vs без
- LRO worker pool sizing: при какой нагрузке Operation становятся stale
- Default-SG inline create overhead на Network.Create latency
- DB connection pool exhaustion при N конкурентных Create

## 2. Когда меня НЕ вызывать

- Generic load testing methodology — это `load-testing-coach`
- Code-level profiling — это developer task
- Functional testing — это `testing-product-coach` или `vpc-newman-author`
- IPAM cascade correctness — это `vpc-cidr-specialist`

## 3. Что я делаю

- k6 script под конкретный сценарий VPC
- Wire up pre-seeded fixtures (Org/Cloud/Folder + Region/Zone + AddressPool)
- Анализ результатов с учётом VPC-специфики:
  - **LRO**: client latency = sync 200ms + Poll-loop, real "completion" латентность считается отдельно
  - **Outbox**: каждый Create пишет в outbox → INSERT в outbox под нагрузкой
  - **IPAM allocator**: 2-фазный (random→sweep) → при full pool — exponential failure rate
  - **Default-SG**: Network.Create вырастает на ~50ms из-за inline create SG
  - **gateway prefix**: api-gateway routing по 3-char prefix → cache hot path

## 4. Что я НЕ делаю

- Не дизайн SLO с нуля — это product owner / SRE
- Не правлю код kacho-vpc для performance — это сервисный разработчик
- Не делаю Newman regression — это `vpc-newman-author`

## 5. VPC-специфичные нагрузочные сценарии

### S1. Network Create burst
Сценарий: N concurrent Network.Create per sec.
Бутылочные горлышки: default-SG inline create, outbox emit (2 INSERT per Network), DB connection pool.
SLO: p99 < 1s, 0 errors, ≥ 50 Create/sec на 1 pod.

### S2. Subnet Create в одной Network
Сценарий: создание subnets с непересекающимися CIDR.
Bottleneck: EXCLUDE constraint на subnets — race detection.
SLO: p99 < 800ms, ≥ 30 Subnet/sec.

### S3. AllocateExternalIP burst
Сценарий: N concurrent AllocateExternalIP под один pool.
Bottleneck: UNIQUE на (pool_id, ip) → 23505 retries.
SLO: p99 < 500ms при < 80% утилизации pool. При ≥ 90% — degradation expected.

### S4. AllocateInternalIP burst в одной Subnet
Сценарий: создать N Address в одной /24 subnet.
Bottleneck: same UNIQUE.
SLO: p99 < 500ms до 200 IP в /24 (≈80% утилизации /24).

### S5. List under heavy data
Сценарий: List Network/Subnet с пагинацией при N=10000 ресурсов в folder.
Bottleneck: cursor pagination, indexes.
SLO: p99 < 300ms на любой странице.

### S6. Mixed read-write (production-like)
Сценарий: 60% Get/List + 30% Create + 10% Delete.
SLO: realistic load profile, p99 < 500ms.

### S7. LRO completion latency
Сценарий: Create → polls until done. Замер latency от Create до done=true.
SLO: 95% завершаются < 2s, 99% < 5s.

### S8. Outbox emit throughput
Сценарий: измерить overhead Create с outbox vs без (требует ENV-флаг
для disable outbox, или сравнение версий).
Result: ожидаемый overhead ≤ 5%.

### S9. Soak 24h
Сценарий: 24h постоянная нагрузка 50 RPS mixed.
Что искать: memory leaks (heap), fd leaks (lsof), pgx pool stats.

### S10. Breakpoint
Сценарий: linear ramp до crash или 3x SLO violation.
Результат: capacity number для capacity planning.

## 6. Pre-test setup для kacho-vpc

| Resource | Действие |
|---|---|
| Folder | Pre-allocated с детерминированным id |
| Region/Zone | Seed `ru-central1` + `ru-central1-{a,b,c,d}` |
| AddressPool | Default pool на zone-a, kind=EXTERNAL_PUBLIC, /16 (для capacity) |
| AuthMode | `dev` для local stress (anonymous = admin), `production` + headers для prod-like |
| DB pool | `KACHO_COMPUTE_DB_MAX_CONNS=50` (vs default 4) для capacity |
| Watch max streams | `KACHO_COMPUTE_WATCH_MAX_STREAMS=32` (default) |

## 7. VPC-специфичные метрики

| Метрика | Источник | Что показывает |
|---|---|---|
| LRO worker queue depth | `operations.Active()` через Prometheus | Сколько Operations в-полете |
| Outbox lag | SELECT max(sequence_no) - last_processed | Backlog event-обработки |
| pgx pool stats | `pgxpool.Stats` экспонированы | Acquire/idle/total |
| IPAM allocator retry rate | `addresses_external_pool_ip_uniq` 23505 count | Утилизация pool |
| Default-SG create latency | timer внутри `network.doCreate` | Inline-create overhead |

## 8. Layout k6 скриптов

```
tests/k6/
├── scripts/
│   ├── lib/
│   │   ├── client.js          — общий HTTP client + auth headers
│   │   ├── fixtures.js        — pre-seed Org/Cloud/Folder/Zone/Pool
│   │   ├── poll-op.js         — LRO polling helper
│   │   └── slo.js             — SLO thresholds (impacted scenarios)
│   ├── network-create-burst.js
│   ├── subnet-create-burst.js
│   ├── allocate-external.js
│   ├── allocate-internal.js
│   ├── list-heavy.js
│   ├── mixed-read-write.js
│   ├── lro-completion.js
│   ├── soak-24h.js
│   └── breakpoint.js
├── environments/
│   ├── local.json             — KIND кластер
│   └── prod.json              — staging / prod-canary
├── results/
│   └── (gitignored, store baselines elsewhere)
└── README.md
```

## 9. SLO (target + подтверждённые значения) для kacho-vpc local

### Целевые SLO (после оптимизаций)

| Операция | p99 latency target | RPS sustained target |
|---|---|---|
| **Create** (Network/Subnet/Address/RT/SG/GW/PE) | **≤ 50ms** | ≥ 1000/sec на 1 pod |
| **Delete** | **≤ 50ms** | ≥ 1000/sec на 1 pod |
| **Read** (Get/List) | **≤ 10ms** | ≥ 3000/sec на 1 pod |
| Update | ≤ 50ms | ≥ 1000/sec |

### Подтверждённые значения (ghz direct gRPC, оптимизированный конфиг)

Конфиг: `synchronous_commit=off`, `KACHO_COMPUTE_DB_MAX_CONNS=280`,
`KACHO_COMPUTE_DEFAULT_SG_INLINE=false`, folder TTL cache, pg_notify trigger
disabled, прямой gRPC к `vpc:9090`.

| Операция | RPS | p50 | p95 | p99 | Errors | Verdict |
|---|---|---|---|---|---|---|
| Network Create | 500 | 0.70ms | 0.91ms | **1.56ms** | 0 | ✅ 32× запас |
| Network Create | 1000 | 0.62ms | 1.00ms | **1.90ms** | 0 | ✅ |
| Network Create | 2000 | 0.72ms | 1.95ms | **3.58ms** | 0 | ✅ |
| Network Create | 3000 | 1.19ms | 2.99ms | **5.94ms** | 0 | ✅ |
| Network Create | ~5437 (burst, no rate-limit) | 44ms | 109ms | **164ms** | 0 | ⚠️ degradation под uncontrolled burst |
| Network Delete | 500 | 0.79ms | 1.03ms | **1.76ms** | 0 | ✅ |
| Network List | 1000 | 0.73ms | 1.00ms | **1.58ms** | 0 | ✅ |
| Network List | 3000 | 1.01ms | 2.69ms | **5.13ms** | 0 | ✅ |
| Network List | 5000 | 1.77ms | 3.53ms | **5.28ms** | 0 | ✅ |
| Network List | ~8000 | 4.11ms | 12.51ms | **18.63ms** | ~0.05% | ⚠️ p99 > 10ms target при 8K RPS |

**Вывод:** при rate-limited production-load profile все SLO target выполнены
с большим запасом. Деградация (Create p99 164ms) наблюдается только при
**uncontrolled burst** (concurrency 300, без RPS-лимита) — это не realistic
production load.

- **Create p99 ≤ 50ms** держится до **~5000 RPS** на 1 pod (на 5437 RPS burst → 164ms).
- **Read p99 ≤ 10ms** держится до **~6000 RPS** на 1 pod (на 8000 RPS → 18.6ms).
- **Delete p99 ≤ 50ms** держится высоко (1.76ms @ 500 RPS).

### Без оптимизаций (naive config, через api-gateway)

| Операция | RPS | p99 | Примечание |
|---|---|---|---|
| Network Create | ~90 (burst) | 4.3s | default-SG inline + sync folder + sync_commit=on + pool=4 |
| List | ~3500 | < 20ms | через api-gateway proxy |

**Это для местного KIND кластера** (1 pod каждого сервиса, dev-машина).
Production показатели зависят от железа, реплик, network path.

## 10. Гетчи специфики Kachō

1. **LRO async**: real "create completed" latency = sync return (фастный) + poll-loop. Не путать с client-perceived latency.
2. **Outbox INSERT**: каждая Create-мутация делает minимум 2 INSERT в TX (resource + outbox). Под нагрузкой это становится bottleneck.
3. **Default-SG**: Network.Create включает inline create default SG (1 extra INSERT + 1 outbox row). +50-100ms к latency.
4. **IPAM 2-фаз**: первые 8 попыток random pick, дальше sweep. На загруженном pool latency растёт нелинейно.
5. **Prefix routing**: `enp*` id маршрутизируется в VPC, `b1g*` в resource-manager. Mixed-suite trip across services.
6. **AuthMode** prod: каждый запрос требует `x-kacho-folder-id`, `x-kacho-actor`. Без них 403.
