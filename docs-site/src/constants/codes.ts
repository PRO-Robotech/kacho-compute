// gRPC-коды ошибок — единый словарь для компонента <Codes />.
// kacho-compute маппит repo-sentinel'ы в gRPC-коды через internal/service/maperr.go;
// формат ошибки (конвенция Kachō) — google.rpc.Status {code, message, details:[]}.
export const CODES = {
  invalidArgument: {
    grpc: 'INVALID_ARGUMENT',
    http: '400',
    when: 'Некорректный аргумент: regex имени, размер диска вне диапазона, неизвестное поле update_mask, immutable-поле в mask, недопустимая resources платформы',
  },
  notFound: {
    grpc: 'NOT_FOUND',
    http: '404',
    when: 'Ресурс с указанным id не существует (well-formed id, но строки нет): Instance / Disk / Image / Snapshot / Disk type',
  },
  alreadyExists: {
    grpc: 'ALREADY_EXISTS',
    http: '409',
    when: 'Нарушение partial UNIQUE — дубль (projectId, name) для ресурса',
  },
  failedPrecondition: {
    grpc: 'FAILED_PRECONDITION',
    http: '400',
    when: 'Состояние не позволяет операцию: «Instance must be stopped», «Instance is not running», «The disk is being used», disk не READY при attach',
  },
  unavailable: {
    grpc: 'UNAVAILABLE',
    http: '503',
    when: 'Недоступен peer-сервис (kacho-iam / kacho-vpc / kacho-geo) при валидации на request-path (fail-closed для мутаций)',
  },
  unauthenticated: {
    grpc: 'UNAUTHENTICATED',
    http: '401',
    when: 'Отсутствует / невалиден JWT (проверяется на api-gateway)',
  },
  permissionDenied: {
    grpc: 'PERMISSION_DENIED',
    http: '403',
    when: 'Субъект не имеет нужного отношения (relation) на ресурс/проект в OpenFGA (authz-интерсептор)',
  },
  internal: {
    grpc: 'INTERNAL',
    http: '500',
    when: 'Внутренняя ошибка БД — текст не раскрывается («internal database error»)',
  },
} as const

export type CodeKey = keyof typeof CODES
