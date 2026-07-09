// Единые формулировки описаний полей — источник для таблиц полей на страницах ресурсов.
// Меняешь смысл поля → правишь здесь, а не в N страницах.
export const DICTIONARY = {
  id: { short: 'Идентификатор ресурса (output-only, генерируется сервером)' },
  projectId: { short: 'Идентификатор проекта (домен kacho-iam); ресурс — project-level' },
  name: { short: 'Имя ресурса (lowercase, 0..63 символа)' },
  description: { short: 'Описание (0..256 символов)' },
  labels: { short: 'Метки key:value (до 64 пар)' },
  createdAt: { short: 'Время создания (RFC 3339, усечено до секунд)' },
  status: { short: 'Текущий статус ресурса (enum)' },
  zoneId: { short: 'Зона доступности (домен kacho-geo); immutable' },
  updateMask: { short: 'Список изменяемых полей (FieldMask)' },
  filter: { short: 'Фильтр списка (name="<value>")' },
  pageSize: { short: 'Размер страницы (0 → 50, максимум 1000)' },
  pageToken: { short: 'Курсор следующей страницы (opaque base64)' },
  // Disk
  typeId: { short: 'Идентификатор типа диска (DiskType)' },
  diskSize: { short: 'Размер диска в байтах' },
  blockSize: { short: 'Размер блока в байтах (по умолчанию 4096); immutable' },
  diskSource: { short: 'Источник диска: imageId или snapshotId (oneof, immutable)' },
  instanceIds: { short: 'Инстансы, к которым присоединён диск (output-only)' },
  // Image
  family: { short: 'Семейство образа — для GetLatestByFamily (immutable)' },
  minDiskSize: { short: 'Минимальный размер диска, создаваемого из образа, в байтах (immutable)' },
  storageSize: { short: 'Занимаемый размер в байтах (output-only)' },
  os: { short: 'Операционная система образа (LINUX | WINDOWS)' },
  imageSource: { short: 'Источник образа: imageId | diskId | snapshotId | uri (oneof, ровно один)' },
  // Snapshot
  sourceDiskId: { short: 'Идентификатор исходного диска (immutable)' },
  diskSizeAtSnapshot: { short: 'Размер диска на момент снимка в байтах (immutable, output-only)' },
  // Instance
  platformId: { short: 'Идентификатор платформы (конфигурации аппаратных ресурсов)' },
  resources: { short: 'Вычислительные ресурсы: cores, memory, coreFraction, gpus' },
  metadata: { short: 'Пользовательская метадата key:value (≤256 KiB); меняется через UpdateMetadata' },
  bootDisk: { short: 'Загрузочный диск инстанса (AttachedDisk)' },
  secondaryDisks: { short: 'Дополнительные присоединённые диски (output-only)' },
  networkInterfaces: { short: 'Сетевые интерфейсы инстанса (denormalised-зеркало NIC из kacho-vpc)' },
  serviceAccountId: { short: 'Сервисный аккаунт для аутентификации внутри инстанса (kacho-iam)' },
  fqdn: { short: 'Доменное имя инстанса (output-only, назначается сервером)' },
  // DiskType
  diskTypeZoneIds: { short: 'Зоны, в которых доступен тип диска' },
} as const

export type DictionaryKey = keyof typeof DICTIONARY
