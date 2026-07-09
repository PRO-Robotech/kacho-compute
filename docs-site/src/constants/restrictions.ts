// Правила валидации полей — единый источник для компонента <Restrictions />.
// Тексты и границы сверены с proto (kacho-proto/.../compute/v1) и
// internal/service/validate.go kacho-compute.
export const RESTRICTIONS = {
  name: [
    'regex ^([a-z]([-_a-z0-9]{0,61}[a-z0-9])?)?$',
    'длина 0..63; только lowercase (строчные буквы, цифры, дефис, подчёркивание)',
    'uppercase запрещён (конвенция имени compute — строже, чем в некоторых других доменах Kachō)',
  ],
  projectId: [
    'обязателен при Create',
    'ссылка на Project домена kacho-iam (существование проверяется вызовом ProjectService.Get)',
    'immutable после Create',
  ],
  zoneId: [
    'обязателен при Create Instance / Disk',
    'ссылка на Zone домена kacho-geo (существование проверяется вызовом ZoneService.Get)',
    'immutable после Create (меняется только через Relocate)',
  ],
  labels: [
    'до 64 пар key:value',
    'ключ: regex [a-z][-_./\\@0-9a-z]*, длина 1..63',
    'значение: regex [-_./\\@0-9a-z]*, длина 0..63',
  ],
  description: ['длина 0..256'],
  diskSize: [
    'размер в байтах',
    'Create: 4194304 (4 MiB) .. 28587302322176 (~26 TiB)',
    'Update: только увеличение и в меньшем верхнем пределе — 4194304 .. 4398046511104 (4 TiB)',
    'уменьшение размера → INVALID_ARGUMENT «Disk size can only be increased»',
  ],
  blockSize: ['размер блока в байтах; по умолчанию 4096', 'immutable после Create'],
  instanceResources: [
    'cores / memory / core_fraction / gpus — валидируются по таблице платформы (platformId)',
    'core_fraction ∈ {0, 5, 20, 50, 100}',
    'изменение cores/memory/platformId — только когда Instance в статусе STOPPED',
  ],
  metadata: [
    'карта key:value; суммарный размер всех ключей и значений < 512 KB, каждое значение ≤ 256 KB',
    'меняется отдельным RPC UpdateMetadata (delete[] + upsert{}), не через Update',
  ],
  updateMask: [
    'google.protobuf.FieldMask — список изменяемых полей',
    'неизвестное поле в mask → INVALID_ARGUMENT',
    'hard-immutable поле в mask → INVALID_ARGUMENT «<field> is immutable after <Resource>.Create»',
    'пустой mask → full-PATCH: применяются все mutable-поля, immutable из тела игнорируются',
  ],
  pagination: [
    'pageSize: 0 → default 50; максимум 1000',
    'pageToken: opaque base64 от (createdAt, id); передавать как есть',
    'garbage-token → INVALID_ARGUMENT',
    'nextPageToken пуст → последняя страница',
  ],
  filter: ['синтаксис фильтрации Kachō; в текущей фазе поддержан предикат name="<value>"'],
  resourceId: [
    'TEXT: 3-символьный префикс + 17 символов crockford-base32 (20 всего)',
    'генерируется сервером (output-only)',
    'well-formed, но несуществующий id → NOT_FOUND',
  ],
} as const

export type RestrictionKey = keyof typeof RESTRICTIONS
