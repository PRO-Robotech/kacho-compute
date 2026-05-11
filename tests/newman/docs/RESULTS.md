# newman — результаты прогона (kacho-compute)

## Статус: v1 — сгенерировано, ещё не прогнано против задеплоенного стенда

Коллекции сгенерированы (`scripts/gen.py`); прогон против live api-gateway **не выполнен** —
на момент создания сьюты compute-backend не задеплоен в локальном стенде
(api-gateway на `localhost:18080` отвечает `503 "name resolver error: produced zero addresses"`
для `/compute/v1/*`; VPC-backend работает). Прогон будет выполнен после `kacho-deploy` с поднятым
`kacho-compute` — результаты заносятся ниже.

## Сводка (v1 — generated)

| Ресурс | Cases | Steps | Assertions* | Failed | Status |
|---|---|---|---|---|---|
| disk | 74 | 204 | — | — | generated, not run |
| instance | 82 | 426 | — | — | generated, not run |
| image | 60 | 149 | — | — | generated, not run |
| snapshot | 52 | 157 | — | — | generated, not run |
| disk-type | 10 | 10 | — | — | generated, not run |
| zone | 10 | 11 | — | — | generated, not run |
| operation | 8 | 18 | — | — | generated, not run |
| **Итого** | **296** | **975** | — | — | — |

\* assertions считаются при прогоне (`run.sh` → `out/<resource>.json`).

## Как прогнать

```bash
# 1. Поднять стенд с задеплоенным compute + port-forward api-gateway → localhost:18080
cd ../../kacho-deploy && make dev-up && make reload-svc SVC=compute
# 2. (если seed e2e-ресурсов VPC отличается от env — поправить environments/local.postman_environment.json:
#     existingNetworkId / existingSubnetId / existingSgId / existingPlatformId)
# 3. Перегенерить коллекции (если меняли cases/*.py)
python3 tests/newman/scripts/gen.py
# 4a. Прогнать всё одним махом
tests/newman/scripts/run.sh                       # сводка → out/summary.txt
tests/newman/scripts/run.sh --service disk        # один ресурс
# 4b. Прогнать ПО ОДНОМУ кейсу за раз с зачисткой ресурсов (quota-safe — как для YC)
tests/newman/scripts/run-incremental.sh           # все ~296 кейсов; сводка → out/incremental/summary.txt
tests/newman/scripts/run-incremental.sh --resume                 # продолжить прерванный
tests/newman/scripts/run-incremental.sh --service instance       # один ресурс
tests/newman/scripts/run-incremental.sh --failed                 # только упавшие из прошлого прогона
tests/newman/scripts/run-incremental.sh --cleanup-only           # стереть throwaway-ресурсы в тест-папках
```

**Деплоймент-замечания:**
- Instance CRUD-кейсы (`INST-*-CRUD-*`, `INST-STATE-*`, `INST-AD/DD/NAT/UMETA-*`, `INST-DISK-DEL-WHILE-ATTACHED`)
  требуют поднятого `kacho-vpc` + seeded `existingNetworkId`/`existingSubnetId`/`existingSgId` в той же зоне (`existingZoneId`).
- `*-NEG-SUBNET-NOTFOUND` / `*-NEG-FOLDER-NOTFOUND` / `OP-GET-CRUD-FAILED-OP` требуют `KACHO_COMPUTE_SKIP_PEER_VALIDATION!=true`
  (при `=true` cross-service existence-checks — no-op → эти кейсы краснеют; помечены `# requires peer-validation enabled` в cases).
- `# probe-needed:` кейсы фиксируют наше текущее поведение там, где точный YC-контракт ещё не verified (список — `REQUIREMENTS.md` §A);
  они написаны с `allow [200,400]` / substr-assert, чтобы не краснеть на любом разумном поведении — заменяются точными assert'ами после probe против реального YC.

## Эволюция

| Версия | Cases | Steps | Что добавлено |
|---|---|---|---|
| **v1** | **296** | **975** | первая версия: disk(74) / instance(82) / image(60) / snapshot(52) / disk-type(10) / zone(10) / operation(8). Полный CRUD + Operations LRO poll, Instance state-машина, attach/detach + Disk-delete-while-attached, NAT, UpdateMetadata, GetLatestByFamily, BVA (size/name/labels/pagesize/cores/core_fraction), CONF (id-prefix epd/fd8, created_at до секунд, Operation.response=Empty, BASIC-view metadata omission), security probes, lifecycle conformance. 100% публичных RPC compute-домена покрыты ≥1 кейсом (кроме явных `blocked:*` / scope-cut — см. TEST-PLAN). |

## Skill-mapping (testing-product-coach §3, §4)

| Техника | Реализация |
|---|---|
| §3.1 ECP | ✅ `name_validation_block`, `labels_validation_block`, `description_validation_block` |
| §3.2 BVA | ✅ disk size 4MiB/below/26TiB/above, name len 63/64, pageSize 0/1/1000/1001, labels 64/65, cores set, core_fraction set |
| §3.3 Decision Tables | ✅ required-field matrix (Instance: zone/platform/resources/bootdisk/nic/folder), UpdateMask (unknown/immutable/empty), error mapping |
| §3.4 State Transition | ✅ Instance state-машина (Start/Stop/Restart preconditions, AttachDisk/DetachDisk/NAT), immutable fields, Disk-delete-while-attached |
| §3.5 Pairwise | partial (Disk size × type × source — частично; full pairwise — backlog) |
| §3.7 Use-case | ✅ `*-LIFECYCLE-CONF` (полный CRUD-цикл; Instance — с Stop/Start) |
| §3.8 Error Guessing | ✅ `malformed_body_block`, empty body, HTTP-method, garbage prefix (Operation) |
| §3.10 Property-Based | ✅ pagination roundtrip (ZONE), idempotent move-self semantics (через MV-кейсы) |
| §3.11 Risk-Based | ✅ priority P0..P3 tagging — P0 на security/data-integrity/state-machine/Disk-delete-while-attached |
| §4.1 Smoke | ✅ P0/P1 кейсы — фактический smoke |
| §4.2 Functional regression | ✅ полная suite (296 кейсов) |
| §4.3 Conformance | ✅ CONF class: id-prefix, created_at до секунд, Operation.response=Empty, NF-text формат, BASIC-view metadata omission; зеркало против YC через `yc-proxy.js` |
| §4.4 Performance | → перенесено в k6 (`tests/k6/`) |
| §4.5-4.8 Load/Stress/Soak/Spike | → k6 |
| §4.10 Security | ✅ `security_injection_block` (SQLi/union/XSS/cmd/path/longpayload × name + filter) |
| §4.11 Compatibility | → backlog |
| §4.12 Migration | covered внешними тестами (`kacho-deploy` smoke) |

## Findings

Найденные баги / расхождения с verbatim YC / observability-gaps — заводятся в GitHub Issues
(`PRO-Robotech/kacho-compute`, метки `bug`/`tech-debt`/`enhancement`; `blocked:kacho-kms` /
`blocked:kacho-marketplace` / `blocked:kacho-snapshot-schedule` / `blocked:kacho-filesystem` для
заблокированного), см. `kacho-compute/CLAUDE.md` §14.4. By-design расхождения — `docs/architecture/07-known-divergences.md`.
Отдельного bug-map / FINDING-реестра нет.
