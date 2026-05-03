package reconciler

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/PRO-Robotech/kacho-corelib/ids"
	"github.com/PRO-Robotech/kacho-corelib/operations"
	"github.com/PRO-Robotech/kacho-compute/internal/config"
	"github.com/PRO-Robotech/kacho-compute/internal/domain"
	"github.com/PRO-Robotech/kacho-compute/internal/repo"
	computev1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/compute/v1"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// InstanceHandler — reconciler для Instance.
type InstanceHandler struct {
	instRepo *repo.InstanceRepo
	diskRepo *repo.DiskRepo
	opsRepo  operations.Repo
	sim      config.SimConfig
	logger   *slog.Logger
}

// NewInstanceHandler создаёт InstanceHandler.
func NewInstanceHandler(
	instRepo *repo.InstanceRepo,
	diskRepo *repo.DiskRepo,
	opsRepo operations.Repo,
	sim config.SimConfig,
	logger *slog.Logger,
) *InstanceHandler {
	return &InstanceHandler{
		instRepo: instRepo,
		diskRepo: diskRepo,
		opsRepo:  opsRepo,
		sim:      sim,
		logger:   logger,
	}
}

// Reconcile обрабатывает одну итерацию reconcile для Instance.
func (h *InstanceHandler) Reconcile(ctx context.Context, inst *domain.Instance) {
	switch inst.Status {
	case domain.InstanceStatusProvisioning:
		h.handleProvisioning(ctx, inst)
	case domain.InstanceStatusStarting:
		h.handleStarting(ctx, inst)
	case domain.InstanceStatusStopping:
		h.handleStopping(ctx, inst)
	case domain.InstanceStatusDeleting:
		h.handleDeleting(ctx, inst)
	}
}

func (h *InstanceHandler) handleProvisioning(ctx context.Context, inst *domain.Instance) {
	min, max := h.sim.ProvisionDuration()
	delay := randDuration(min, max)
	h.logger.Info("instance provisioning", "id", inst.ID, "delay_ms", delay.Milliseconds())

	select {
	case <-ctx.Done():
		return
	case <-time.After(delay):
	}

	now := time.Now().UTC()
	inst.Status = domain.InstanceStatusRunning
	inst.ObservedGeneration = inst.Generation
	inst.StatusLastTransitionAt = now
	inst.IPs = domain.Ips{
		Internal: []string{generateInternalIP()},
	}

	updated, err := h.instRepo.Update(ctx, inst)
	if err != nil {
		h.logger.Error("instance provisioning: update failed", "id", inst.ID, "err", err)
		return
	}

	// Найти и завершить операцию Create/Update для этого инстанса.
	if err := h.markOperationDone(ctx, inst.ID, updated); err != nil {
		h.logger.Error("instance provisioning: markDone failed", "id", inst.ID, "err", err)
	}
}

func (h *InstanceHandler) handleStarting(ctx context.Context, inst *domain.Instance) {
	min, max := h.sim.StartStopDuration()
	delay := randDuration(min, max)

	select {
	case <-ctx.Done():
		return
	case <-time.After(delay):
	}

	now := time.Now().UTC()
	inst.Status = domain.InstanceStatusRunning
	inst.ObservedGeneration = inst.Generation
	inst.StatusLastTransitionAt = now

	updated, err := h.instRepo.Update(ctx, inst)
	if err != nil {
		h.logger.Error("instance starting: update failed", "id", inst.ID, "err", err)
		return
	}

	if err := h.markOperationDone(ctx, inst.ID, updated); err != nil {
		h.logger.Error("instance starting: markDone failed", "id", inst.ID, "err", err)
	}
}

func (h *InstanceHandler) handleStopping(ctx context.Context, inst *domain.Instance) {
	min, max := h.sim.StartStopDuration()
	delay := randDuration(min, max)

	select {
	case <-ctx.Done():
		return
	case <-time.After(delay):
	}

	now := time.Now().UTC()

	// Если DesiredPowerState=RUNNING — это была остановка для restart, переходим в STARTING.
	if inst.DesiredPowerState == domain.PowerStateRunning {
		// Restart cycle: STOPPING → STARTING
		inst.Status = domain.InstanceStatusStarting
		inst.StatusLastTransitionAt = now
		// Запишем время как последнего restart.
		inst.LastRestartCompletedAt = &now

		if _, err := h.instRepo.Update(ctx, inst); err != nil {
			h.logger.Error("instance restart-stopping: update failed", "id", inst.ID, "err", err)
		}
		// Не markDone — reconciler подхватит STARTING на следующей итерации.
		return
	}

	// Обычная остановка: STOPPING → STOPPED.
	inst.Status = domain.InstanceStatusStopped
	inst.ObservedGeneration = inst.Generation
	inst.StatusLastTransitionAt = now

	updated, err := h.instRepo.Update(ctx, inst)
	if err != nil {
		h.logger.Error("instance stopping: update failed", "id", inst.ID, "err", err)
		return
	}

	if err := h.markOperationDone(ctx, inst.ID, updated); err != nil {
		h.logger.Error("instance stopping: markDone failed", "id", inst.ID, "err", err)
	}
}

func (h *InstanceHandler) handleDeleting(ctx context.Context, inst *domain.Instance) {
	// Открепляем диски.
	for _, sd := range inst.SecondaryDisks {
		if sd.DiskID == "" {
			continue
		}
		disk, err := h.diskRepo.Get(ctx, sd.DiskID)
		if err != nil {
			continue
		}
		disk.AttachedToInstanceID = ""
		if _, err := h.diskRepo.Update(ctx, disk); err != nil {
			h.logger.Error("deleting: detach secondary disk", "disk_id", sd.DiskID, "err", err)
		}
	}

	// Физическое удаление (boot disk с auto_delete тоже удаляем).
	if inst.BootDisk.DiskID != "" && inst.BootDisk.AutoDelete {
		if err := h.diskRepo.HardDelete(ctx, inst.BootDisk.DiskID); err != nil {
			h.logger.Error("deleting: delete boot disk", "disk_id", inst.BootDisk.DiskID, "err", err)
		}
	}

	if err := h.instRepo.HardDelete(ctx, inst.ID); err != nil {
		h.logger.Error("instance deleting: hard delete failed", "id", inst.ID, "err", err)
		return
	}

	// Завершаем операцию Delete.
	// Так как инстанс удалён, передаём пустой ответ.
	emptyInst := &computev1.Instance{Id: inst.ID}
	resp, _ := anypb.New(emptyInst)
	if err := h.markOperationDoneWithResp(ctx, inst.ID, resp); err != nil {
		h.logger.Error("instance deleting: markDone failed", "id", inst.ID, "err", err)
	}
}

// markOperationDone находит незавершённую операцию для ресурса и вызывает MarkDone.
func (h *InstanceHandler) markOperationDone(ctx context.Context, instanceID string, inst *domain.Instance) error {
	resp, err := anypb.New(domainInstanceToProto(inst))
	if err != nil {
		return err
	}
	return h.markOperationDoneWithResp(ctx, instanceID, resp)
}

func (h *InstanceHandler) markOperationDoneWithResp(ctx context.Context, instanceID string, resp *anypb.Any) error {
	ops, _, err := h.opsRepo.List(ctx, operations.ListFilter{
		ResourceID: instanceID,
		PageSize:   10,
	})
	if err != nil {
		return fmt.Errorf("list ops: %w", err)
	}
	for _, op := range ops {
		if op.Done {
			continue
		}
		if err := h.opsRepo.MarkDone(ctx, op.ID, resp); err != nil {
			return fmt.Errorf("mark done op %s: %w", op.ID, err)
		}
		break // помечаем только первую незавершённую
	}
	return nil
}

// generateInternalIP — имитация назначения IP reconciler-ом.
func generateInternalIP() string {
	uid := ids.NewUID()
	// детерминированный fake: берём первые 12 символов UUID для формирования IP
	return fmt.Sprintf("10.%d.%d.%d",
		int(uid[0])%256,
		int(uid[1])%256,
		int(uid[2])%256,
	)
}

// domainInstanceToProto конвертирует domain.Instance в proto для сохранения в operation response.
func domainInstanceToProto(inst *domain.Instance) *computev1.Instance {
	p := &computev1.Instance{
		Id:                 inst.ID,
		FolderId:           inst.FolderID,
		CreatedAt:          timestamppb.New(inst.CreatedAt),
		Name:               inst.Name,
		Description:        inst.Description,
		Labels:             inst.Labels,
		ZoneId:             inst.ZoneID,
		PlatformId:         inst.PlatformID,
		Status:             computev1.Status(inst.Status),
		Fqdn:               inst.FQDN,
		Metadata:           inst.Metadata,
		DesiredPowerState:  computev1.PowerState(inst.DesiredPowerState),
		Generation:         inst.Generation,
		ResourceVersion:    inst.ResourceVersion,
		ObservedGeneration: inst.ObservedGeneration,
		Ips: &computev1.Ips{
			Internal: inst.IPs.Internal,
			External: inst.IPs.External,
		},
	}
	if inst.Resources.Cores > 0 || inst.Resources.Memory != "" {
		p.Resources = &computev1.Resources{
			Cores:        inst.Resources.Cores,
			Memory:       inst.Resources.Memory,
			CoreFraction: inst.Resources.CoreFraction,
			Gpus:         inst.Resources.GPUs,
		}
	}
	return p
}
