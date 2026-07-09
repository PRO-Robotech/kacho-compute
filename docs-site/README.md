# Kachō Compute — docs-site

Публичная документация сервиса **kacho-compute** (Instance / Disk / Image / Snapshot /
DiskType) на Docusaurus 3. RU-locale, тёмная тема по умолчанию, оформление в палитре
kacho-ui (AntD-flavored).

## Локальный запуск

```bash
npm ci
npm start           # dev-сервер с hot-reload на http://localhost:3000
npm run build       # статическая сборка в build/ (onBrokenLinks: throw — гейт)
npm run serve       # отдать собранный build/ локально
```

`npm run build` — обязательный гейт перед коммитом: он ловит невалидный MDX/JSX,
незакрытый mermaid и битые внутренние ссылки (`onBrokenLinks: 'throw'`).

## Структура

```
docs/
├── intro.mdx                 # что за сервис, ресурсы, ID-префиксы
├── getting-started.mdx       # сквозной сценарий (Disk → Image → Instance)
├── architecture/             # overview, data-model, instance-lifecycle, operations, authz
├── install/                  # deploy, configuration
└── api/                      # overview + страница на ресурс (instance/disk/image/snapshot/disk-type) + operations
src/
├── components/commonBlocks/  # ApiOperation, Codes, Restrictions, StatusTable
└── constants/                # codes.ts, restrictions.ts, types.ts, dictionary.ts
```

Данные (коды ошибок, ограничения полей, словарь описаний) вынесены в `src/constants` —
страницы рендерят их компонентами. Меняешь контракт → правишь константу, а не N страниц.

## Docker

```bash
docker build -t kacho-compute-docs:dev .   # multi-stage: npm build → nginx
```

nginx отдаёт статический `build/` на порту `8080` (`/healthz` — liveness/readiness).
Helm-чарт — в `deploy/`.
