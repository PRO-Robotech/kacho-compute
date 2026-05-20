# GitHub Actions workflows — kacho-compute

## docker-build.yml — DockerHub multi-arch image build (KAC-127)

Собирает Docker-образ `kacho-compute` под `linux/amd64` + `linux/arm64` и
публикует multi-arch manifest в DockerHub. Дополняет `ci.yaml` (build/vet/test/lint),
не заменяет его.

### Триггеры

- push в `main`
- push в `KAC-*` (epic / feature ветки)
- push тегов `v[0-9]+.[0-9]+.[0-9]+` и `...rc[0-9]+`

### Образы и теги

| Образ | Теги |
|---|---|
| `<DOCKERHUB_USERNAME>/kacho-compute` | `<branch>-<sha8>` (multiarch), `amd64-<branch>-<sha8>`, `arm64-<branch>-<sha8>` |

`kacho-compute` — образ включает binary `kacho-compute` (serve).

### Требуемые GitHub secrets

| Secret | Назначение |
|---|---|
| `DOCKERHUB_USERNAME` | Docker Hub username (он же namespace для образов) |
| `DOCKERHUB_TOKEN` | Docker Hub access token (scope: Read/Write/Delete) |

Креды одинаковые для всех `kacho-*` репозиториев (один Docker Hub-аккаунт).

### Установка secrets (user-action)

```bash
gh secret set DOCKERHUB_USERNAME --body "<value>" --repo PRO-Robotech/kacho-compute
gh secret set DOCKERHUB_TOKEN    --body "<value>" --repo PRO-Robotech/kacho-compute
```

### Polyrepo build

`kacho-compute` — часть polyrepo: `go.mod` использует `replace ../kacho-corelib`,
`../kacho-proto`; `Dockerfile` делает `COPY kacho-corelib` / `COPY kacho-proto`.
Workflow чекаутит main-репо + siblings (`kacho-corelib`, `kacho-proto`) в один
каталог; build context = этот каталог. Siblings пиннятся к `ref: KAC-127` —
после merge зависимостей в `main` вернуть на `ref: main`.

### arm64 runner (GitHub-hosted)

Job `docker-build-arm64` гоняется на GitHub-hosted native ARM64-раннере
`runs-on: ubuntu-24.04-arm` (KAC-127). Раньше использовался `self-hosted`
раннер — при offline/busy он вешал job в `pending` бесконечно и `build arm64`
check никогда не зеленел. Hosted ARM64-раннер native — QEMU-эмуляция для
`linux/arm64` не нужна (шаг `Enable QEMU emulation` в arm64-job убран).

## newman-e2e.yml — self-contained newman E2E authz gate (KAC-127)

Полный Newman authz E2E (288-кейсовая default-deny матрица + 30-кейсовая
ServiceAccount/API-token матрица) гоняется **прямо в CI этого репо**: workflow
`newman-e2e.yml` поднимает реальный kind + helm umbrella-стек (Postgres + Ory +
OpenFGA + api-gateway + iam + vpc + compute) на локальном kind-кластере, сидит
shared authz-фикстуры и гоняет сьюты `kacho-iam` через REST api-gateway.

Раньше этот разрыв закрывал `newman-trigger.yml` — он слал `repository_dispatch`
в `kacho-deploy` и требовал вручную заданный PAT `WORKFLOW_DISPATCH_TOKEN`. Без
секрета job молча скипался (`guard` → `has_token=false`), и Newman фактически
не гонялся на PR. `newman-e2e.yml` **не требует никаких секретов**: весь стек
билдится и поднимается в одном job на локальном kind — authz-матрица здесь
реальный блокирующий гейт.

### Триггеры

- `pull_request` в `main`
- `push` в `main`
- `workflow_dispatch` (ручной прогон)

### Что делает

1. Checkout этого репо (ref под тестом) + sibling-репо (`kacho-deploy`,
   `kacho-corelib`, `kacho-proto`, `kacho-vpc`, `kacho-iam`, `kacho-compute`,
   `kacho-api-gateway`, `kacho-workspace`) на `ref: main` (KAC-127 смержен —
   pin снят).
2. Билд всех `kacho-*:dev` образов, `kind load`.
3. `helm install` umbrella (`values.dev.yaml`), ожидание openfga-bootstrap.
4. Сид shared authz-фикстур + прогон 2 newman-сьют (`authz-deny`,
   `authz-sa-apitoken`) через port-forward api-gateway.
5. `assert authz suites green` — fail job если хоть один assertion красный.

Тяжёлый (~15-30 мин) — отдельный workflow, не в быстром `ci.yaml`.

### Секреты

Не требуются. `kacho-ui` — приватный репо, его checkout best-effort
(`continue-on-error`), helm-чарт стабится если checkout не прошёл.

`kacho-deploy/.github/workflows/newman-e2e.yml` остаётся как есть (он
self-contained и гоняется на push/PR в сам `kacho-deploy`).
