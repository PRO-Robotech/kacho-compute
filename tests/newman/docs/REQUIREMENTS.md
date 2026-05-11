# REQUIREMENTS — testability / contract-clarification backlog (kacho-compute newman)

Бэклог **улучшений тестируемости** и **запросов на уточнение контракта** — НЕ нормативный
(нормативный регламент — `PRODUCT-REQUIREMENTS.md`). Здесь: что нужно verified против реального
YC Compute API, чтобы заменить плейсхолдеры `# probe-needed:` в `cases/*.py` на точные assert'ы.

Найденные баги/расхождения — НЕ здесь, а GitHub Issues `PRO-Robotech/kacho-compute` (метки `bug`/`tech-debt`),
см. `kacho-compute/CLAUDE.md` §14.4. By-design расхождения — `docs/architecture/07-known-divergences.md`.

## A. Probe-needed против реального YC Compute API

| # | Что probed | Где в кейсах | Текущая формулировка (placeholder) | Желаемое |
|---|---|---|---|---|
| PROBE-01 | Текст `NotFound` для Disk/Image/Snapshot/Instance/DiskType/Zone Get garbage id | `*-GET-CONF-NF-TEXT`, `DT/ZONE-GET-CONF-NF-TEXT` | substr `"not found"` | точная формулировка `^<Resource> <id> not found$` (как у kacho-vpc) → regex-assert |
| PROBE-02 | Текст `NotFound` для Operation Get garbage epd-id | `OP-GET-CONF-NF-TEXT` | substr `"not found"` | `^Operation <id> not found$` или verbatim YC |
| PROBE-03 | Текст `ALREADY_EXISTS` для duplicate `(folder, name)` | `*-CR-NEG-DUP-NAME` | только code 6 | verbatim YC text → assert |
| PROBE-04 | Текст `InvalidArgument` для unknown disk type_id (и: NotFound vs InvalidArgument?) | `DISK-CR-NEG-TYPE-UNKNOWN` | code 5, substr `"disk type"` | точный code + text |
| PROBE-05 | unknown zone_id в Disk.Create / Disk.Relocate: `InvalidArgument` (наш паритет VPC) vs `NotFound "Zone ... not found"` (YC?) | `DISK-CR-NEG-ZONE-UNKNOWN`, `DISK-REL-NEG-DEST-ZONE-UNKNOWN` | allow code 3\|5 | зафиксировать YC-поведение → single code |
| PROBE-06 | Текст `InvalidArgument` для Disk.Update size-decrease | `DISK-UPD-SIZE-DECREASE-REJECT` | code 3 | `"Disk size can only be increased"` или verbatim YC |
| PROBE-07 | `FailedPrecondition` тексты Instance state-машины: Start-from-RUNNING, Stop-from-STOPPED, Restart-from-STOPPED | `INST-STATE-*` | code 9 | verbatim: "Instance is already running" / "Instance is not running" / ... |
| PROBE-08 | `FailedPrecondition` текст для Instance.Update {resources_spec/platform_id} на RUNNING | `INST-UPD-RESOURCES-REQUIRES-STOPPED` | code 9 | `"Instance must be stopped"` или verbatim |
| PROBE-09 | `FailedPrecondition` текст для DetachDisk boot disk | `INST-DD-NEG-BOOT` | code 9 | `"Cannot detach boot disk"` или verbatim |
| PROBE-10 | `FailedPrecondition` текст для Disk.Delete-while-attached | `INST-DISK-DEL-WHILE-ATTACHED` | code 9 | `"The disk <id> is being used"` или verbatim |
| PROBE-11 | AttachDisk wrong-zone / already-attached: `InvalidArgument` vs `FailedPrecondition`; тексты | `INST-AD-NEG-*` | allow code 3\|9 | зафиксировать |
| PROBE-12 | AddOneToOneNat already-NAT: code + text | `INST-NAT-ADD-NEG-ALREADY` | code 9 | verbatim |
| PROBE-13 | SimulateMaintenanceEvent: возвращает Operation или RPC Unimplemented? | `INST-SME-CRUD-OK` | allow 200\|501 | зафиксировать поведение |
| PROBE-14 | OperationService.Cancel на done-op: `FailedPrecondition` vs idempotent 200 (с уже-done op) | `OP-CANCEL-NEG-ALREADY-DONE` | allow 200\|400 | зафиксировать |
| PROBE-15 | DiskType/Zone List игнорируют ли `page_token`? (справочники малы) | `DT-LST-PAGE-TOKEN-GARBAGE` | allow 200\|400 | зафиксировать |
| PROBE-16 | Compute name regex: точное поведение для пустой строки и edge (одна буква, trailing `_`) | `*-CR-VAL-NAME-EMPTY-OK`, `*-CR-BVA-NAME-*` | empty→200, len63→200, len64→400 | verified contract → `corevalidate.NameCompute` |
| PROBE-17 | malformed/wrong-prefix id (Get/Update/...): YC даёт `InvalidArgument "invalid <res> id '<X>'"`? | (не покрыто отдельным кейсом — паритет VPC gotcha #1) | у нас → `NotFound` | если YC → InvalidArgument: завести divergence + кейс |
| PROBE-18 | Image.Create min_disk_size default (когда не указан) — вычисляется из source? | `IMG-CR-CRUD-OK` (assert minDiskSize > 0) | `> 0` | точная семантика |
| PROBE-19 | Instance fqdn формат при hostname не указан (`<id>.auto.internal`?) | `INST-CR-CRUD-OK` (assert fqdn non-empty) | non-empty string | regex-assert на формат |
| PROBE-20 | Instance.List view=BASIC — какие именно поля опускаются кроме metadata? | `INST-LST-VIEW-BASIC-NO-METADATA` | только metadata | полный список (verbatim YC) |

## B. Тестируемость инфраструктуры

| # | Запрос | Зачем |
|---|---|---|
| TEST-01 | e2e-seed в `kacho-deploy` должен создавать (или env должен документировать) реальные `existingNetworkId`/`existingSubnetId`/`existingSgId` в той же зоне что `existingZoneId` (ru-central1-a) | без них Instance CRUD-кейсы краснеют (нет subnet → NIC-валидация fail) |
| TEST-02 | Документировать в e2e-config: `KACHO_COMPUTE_SKIP_PEER_VALIDATION` (true в test-стенде без VPC/RM?) | от этого зависит, сработают ли `*-NEG-SUBNET-NOTFOUND` / `*-NEG-FOLDER-NOTFOUND` / `OP-GET-CRUD-FAILED-OP` |
| TEST-03 | `existingPlatformId=standard-v3` должен быть в seeded таблице платформ (`internal/service/platforms.go`); если другой набор — поправить env | Instance.Create требует валидный platform_id + соответствующий cores-set |
| TEST-04 | `existingDiskTypeId=network-ssd` присутствует в seed (✓ — `0001_initial.sql`); проверить что доступен в `existingZoneId` | Disk/Instance boot-disk создаются с этим типом |
| TEST-05 | Желательно: api-gateway должен резолвить `compute` backend (сейчас на :18080 — 503 "name resolver error: produced zero addresses" — compute не задеплоен) | без задеплоенного compute сьюту нельзя прогнать |
| TEST-06 | Зеркало против реального YC (`scripts/yc-proxy.js`): нужна выделенная throwaway-folder в YC + реальные network/subnet/SG в ней; подставить в `environments/yc.postman_environment.json` (placeholders `REPLACE-WITH-REAL-YC-*`) | parity-аудит — всё, что ≠ YC = баг |

## C. Покрытие, которое стоит добавить (enhancement, не блокирует)

| # | Что | Почему отложено |
|---|---|---|
| COV-01 | Instance.AttachNetworkInterface / DetachNetworkInterface — happy path | нужен 2-й subnet из kacho-vpc в seed (есть только NEG sync-NF) |
| COV-02 | Disk.Create from-snapshot — full happy path (создать snapshot → disk из него → assert source_snapshot_id) | покрыто частично (DISK-CR-NEG-SOURCE-SNAPSHOT-NOTFOUND); полный happy похож на DISK-CR-CRUD-FROM-IMAGE-OK |
| COV-03 | Disk size ≥ image.min_disk_size validation (Create disk из image с size < min → InvalidArgument) | нужен image с известным min_disk_size; зависит от PROBE-18 |
| COV-04 | block_size whitelist (Disk.Create с block_size не из {4096,8192,...} → InvalidArgument) | зависит от PROBE — точный set неизвестен |
| COV-05 | Instance с 2 NIC / 2 secondary disks (multi-spec) | усложняет cleanup; базовый 1-NIC покрыт |
| COV-06 | Pagination round-trip для Disk/Image/Snapshot/Instance (создать N+1 ресурс → page через token) | quota-heavy; ZONE-LST-PAGE-ROUNDTRIP покрывает паттерн на справочнике |
| COV-07 | access-bindings RPC (no-op skeleton) — smoke-кейс | после реализации AAA |
| COV-08 | `kms_key_id` / `os_product_ids` — `blocked:kacho-kms` / `blocked:kacho-marketplace` | нужны соответствующие сервисы |
| COV-09 | `Disk.ListSnapshotSchedules` — `blocked:kacho-snapshot-schedule` | нужен SnapshotSchedule-ресурс |
| COV-10 | `Instance.AttachFilesystem/DetachFilesystem` — `blocked:kacho-filesystem`; `Instance.Relocate` — `blocked` | нужны Filesystem-ресурс / cross-zone disk move |
