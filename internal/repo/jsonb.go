package repo

import (
	"encoding/json"
	"fmt"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/PRO-Robotech/kacho-compute/internal/service"
)

// marshalJSONB сериализует v в JSONB-байты. Возвращает обёрнутую service.ErrInternal
// при ошибке. Парная форма к unmarshalJSONB. Зеркалит kacho-vpc/internal/repo/jsonb.go.
func marshalJSONB(v any, field string) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("%w: marshal JSONB %s: %v", service.ErrInternal, field, err)
	}
	return b, nil
}

// unmarshalJSONB десериализует JSONB-байты в target. Возвращает обёрнутую
// service.ErrInternal при ошибке. nil/empty raw — no-op.
func unmarshalJSONB(raw []byte, target any, field string) error {
	if len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return fmt.Errorf("%w: corrupted JSONB %s: %v", service.ErrInternal, field, err)
	}
	return nil
}

// marshalProtoJSONB сериализует proto-сообщение в JSONB через protojson (для
// nested-полей вроде HardwareGeneration/KMSKey/DiskPlacementPolicy/...). nil → NULL.
func marshalProtoJSONB[T proto.Message](m T, field string) ([]byte, error) {
	if any(m) == nil || isNilPtr(m) {
		return nil, nil
	}
	b, err := protojson.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("%w: marshal proto JSONB %s: %v", service.ErrInternal, field, err)
	}
	return b, nil
}

// unmarshalProtoJSONB десериализует JSONB-байты в proto-сообщение (target должен
// быть ненулевым указателем). nil/empty raw — no-op.
func unmarshalProtoJSONB(raw []byte, target proto.Message, field string) error {
	if len(raw) == 0 {
		return nil
	}
	if err := protojson.Unmarshal(raw, target); err != nil {
		return fmt.Errorf("%w: corrupted proto JSONB %s: %v", service.ErrInternal, field, err)
	}
	return nil
}

// isNilPtr сообщает, является ли proto-сообщение nil либо typed-nil ((*Foo)(nil)).
func isNilPtr[T proto.Message](m T) bool {
	// proto.Message — интерфейс; m может быть typed-nil (*Foo)(nil) — отлавливаем.
	return m.ProtoReflect() == nil || !m.ProtoReflect().IsValid()
}
