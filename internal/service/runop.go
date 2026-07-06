// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package service

import (
	"context"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"

	"github.com/PRO-Robotech/kacho-corelib/ids"
	"github.com/PRO-Robotech/kacho-corelib/operations"
)

// runOp — единая обёртка async-LRO dispatch: operations.New → opsRepo.Create →
// operations.Run(worker) → возврат синхронного snapshot'а Operation (done=false).
// Устраняет дублирование этой 6-строчной обвязки в каждом мутирующем RPC (Create/
// Update/Delete/Restart/Attach/Detach/UpdateMetadata/Relocate/SimulateMaintenance/
// lifecycle) — изменение контракта диспетчеризации (audit-tag, per-op deadline,
// metric) правится в ОДНОМ месте, а не в скопированных блоках. Мандатный
// async-Operation-паттерн (ban 9) и wire-контракт (LRO envelope, metadata-типы,
// error-mapping, outbox-emit) сохранены дословно — централизуется только
// hand-copied обвязка. desc/meta/worker — единственная per-site вариация; синхронную
// pre-валидацию (guard'ы id/req) call-site выполняет ДО вызова runOp.
func runOp(ctx context.Context, opsRepo operations.Repo, desc string, meta proto.Message,
	fn func(context.Context) (*anypb.Any, error)) (*operations.Operation, error) {
	op, err := operations.New(ids.PrefixOperationCompute, desc, meta)
	if err != nil {
		return nil, err
	}
	if err := opsRepo.Create(ctx, op); err != nil {
		return nil, err
	}
	operations.Run(ctx, opsRepo, op.ID, fn)
	return &op, nil
}
