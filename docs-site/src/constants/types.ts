// Строковые обозначения proto-типов — для колонки «Тип» в таблицах полей.
export const TYPES = {
  string: 'string',
  int64: 'int64',
  bool: 'bool',
  timestamp: 'google.protobuf.Timestamp',
  fieldMask: 'google.protobuf.FieldMask',
  duration: 'google.protobuf.Duration',
  mapStringString: 'map<string,string>',
  repeatedString: 'repeated string',
  status: 'enum Status',
} as const

export type TypeKey = keyof typeof TYPES
