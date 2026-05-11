# tests/newman — публичный API kacho-compute, regression suite

**Главная regression-инфраструктура** kacho-compute (`tests/newman/`; рядом `tests/k6/` —
нагрузочные сценарии). Black-box покрытие всех публичных RPC compute-домена через HTTP
api-gateway. Спроектирована по `testing-product-coach` (формальные техники test design) с
naming/structure по `testing-code-coach`. Структура — копия `../kacho-vpc/tests/newman/`.
Источник истины — декларативные case-файлы `cases/*.py`; коллекции в `collections/`
**генерируются** скриптом `scripts/gen.py`.

> Критерий приёмки: **любой compute-кейс должен зеленеть и против реального YC Compute API**
> (verbatim parity). Кейсы с `# probe-needed:` фиксируют наше текущее поведение там, где точный
> YC-контракт ещё не verified — список — в `docs/REQUIREMENTS.md` §A.

## Структура

```
tests/newman/
├── README.md                — этот файл
├── cases/                   — ИСТОЧНИК ИСТИНЫ: декларативные case-наборы (Python), по ресурсу
│   ├── disk.py / image.py / snapshot.py / instance.py   — мутируемые ресурсы (Disk/Image/Snapshot/Instance)
│   ├── disk-type.py / zone.py                            — read-only справочники
│   └── operation.py                                     — OperationService (Get/Cancel через api-gateway OpsProxy)
├── collections/             — СГЕНЕРИРОВАННЫЕ Postman-коллекции (по ресурсу) — НЕ править руками
│   └── {…}.postman_collection.json
├── environments/
│   ├── local.postman_environment.json   — local stand (port-forward api-gateway → 18080)
│   └── yc.postman_environment.json       — реальный Yandex Cloud (baseUrl → yc-proxy на :18081)
├── scripts/
│   ├── gen.py                — генератор коллекций из cases/* (Postman v2.1 JSON)
│   ├── run.sh                — прогон одного/всех ресурсов целиком (newman + JSON reporter → out/)
│   ├── run-incremental.sh    — прогон ПО ОДНОМУ кейсу за раз + зачистка ресурсов после каждого (quota-safe, как для YC); --resume / --failed / --cases / --cleanup-only
│   ├── run-incremental.js    — драйвер (newman library API — без per-case process startup; env SERVICES=... / CASES=... ограничивает список)
│   └── yc-proxy.js           — локальный reverse-proxy для прогона против реального YC: /compute/v1/*→compute.api, /operations/*→operation.api, /resource-manager/*→resource-manager.api, /vpc/v1/*→vpc.api, подставляет Bearer (yc iam create-token)
├── docs/
│   ├── TAXONOMY.md            — классы кейсов и naming convention
│   ├── TEST-PLAN.md           — карта покрытия (RPC × класс)
│   ├── CASES-INDEX.md         — каталог кейсов + уникальные паттерны
│   ├── PRODUCT-REQUIREMENTS.md — НОРМАТИВНЫЙ регламент REQ-* (от QA; compute-yc-parity-auditor проверяет соответствие)
│   ├── REQUIREMENTS.md        — бэклог *улучшений* (testability / probe-needed asks — не нормативный)
│   └── RESULTS.md             — последний прогон pass/fail + история версий + skill-mapping
└── out/                     — newman raw output + summary.txt (gitignored snap-логи)
```
(Найденные дефекты/наблюдения — в GitHub Issues `PRO-Robotech/kacho-compute`, см. `kacho-compute/CLAUDE.md` §14.4;
by-design расхождения с verbatim YC — `docs/architecture/07-known-divergences.md`. Отдельного bug-map нет.)

## Быстрый старт

```bash
# 1. Поднять стенд с задеплоенным compute + port-forward api-gateway → localhost:18080
cd ../../kacho-deploy && make dev-up && make reload-svc SVC=compute
# 2. Перегенерить коллекции из cases/*.py (если меняли cases или код)
python3 scripts/gen.py            # все ресурсы; или: python3 scripts/gen.py disk
# 3a. Прогнать всё одним махом (быстро; во время прогона создаётся много ресурсов разом)
./scripts/run.sh                  # сводка → out/summary.txt
./scripts/run.sh --service disk   # один ресурс
# 3b. Прогнать ПО ОДНОМУ кейсу за раз с зачисткой ресурсов после каждого
#     (низкий resource-footprint в любой момент → безопасно при quota-guard, как у YC)
./scripts/run-incremental.sh                        # все ~296 кейсов; сводка → out/incremental/summary.txt
./scripts/run-incremental.sh --resume               # продолжить прерванный прогон
./scripts/run-incremental.sh --service instance     # один ресурс
./scripts/run-incremental.sh --failed               # только упавшие из прошлого прогона (после фикса)
./scripts/run-incremental.sh --cases DISK-CR-CRUD-OK,INST-STATE-STOP-OK   # явный список кейсов
./scripts/run-incremental.sh --cleanup-only         # просто стереть throwaway-ресурсы в тест-папках
#     тюнинг через env: CLEANUP_EVERY (как часто periodic-cleanup, default 25), DELAY_REQUEST (ms, default 30), SERVICES='r1 r2 ...'
```

## Прогон против реального YC Compute API (parity-аудит)

Всё, что ≠ YC, считаем багом (или намеренным divergence — в `docs/architecture/07-known-divergences.md`).
Нужен сконфигурированный `yc` CLI и выделенная throwaway-folder в YC (cleanup-pass стирает в ней
все instances/snapshots/images/disks). Реальные network/subnet/SG в этой folder — создать заранее
и подставить в `environments/yc.postman_environment.json` (placeholders `REPLACE-WITH-REAL-YC-*`).

```bash
node scripts/yc-proxy.js &                            # локальный reverse-proxy :18081 (compute.api / operation.api / resource-manager.api / vpc.api + Bearer)
#   в environments/yc.postman_environment.json подставь свою throwaway-folder в existingFolderId/CrossId + реальные network/subnet/SG
ENV=environments/yc.postman_environment.json \
  SERVICES='disk image snapshot instance disk-type zone operation' \
  ./scripts/run-incremental.sh                        # результат → out/incremental/{progress.tsv, summary.txt, failed/<id>.json}; упавшие = расхождения с YC
```

## Принципы (из testing-product-coach)

- **Black-box**: тестируем продукт через публичный gRPC/REST api-gateway, не код. Тест не знает о
  SQLSTATE, имени constraint'а, конкретной БД.
- **Источник истины**: acceptance-spec + proto-определения (`kacho-proto/.../compute/v1/`) + reference YC.
- **Изоляция**: каждый case-сценарий внутри своего `runId`; suite внутри pre-allocated
  `existingFolderId`/`existingFolderCrossId` (env), Org/Cloud/Folder **не создаёт**; имена суффиксуются `{{runId}}`.
- **LRO-poll**: каждая мутация (`Create/Update/Delete/Move/Relocate/Start/Stop/Restart/Attach/Detach/NAT/UpdateMetadata`)
  → `Operation` → poll `GET /operations/{id}` (retry до 8 раз через `setNextRequest`) до `done=true` → assert `response`/`error`.
- **Формальные техники**: ECP, BVA, decision tables, state transition, error guessing, security — все классы кейсов выводятся системно.
- **Conformance**: каждый кейс должен зеленеть и против YC (`--env yc` через yc-proxy).
- **Risk-prioritization**: high-risk зоны (security, data-integrity FK, Instance state-машина, Disk-delete-while-attached) — P0, больше кейсов.

См. подробности в `docs/TAXONOMY.md`. Cross-service зависимости (Instance NIC → kacho-vpc subnet/SG;
folder → resource-manager) и флаг `KACHO_COMPUTE_SKIP_PEER_VALIDATION` — см. там же и в `docs/RESULTS.md` §«Деплоймент-замечания».
