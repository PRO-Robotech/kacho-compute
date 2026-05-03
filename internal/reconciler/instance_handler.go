package reconciler

import (
	"context"
	"math/rand"
	"net"
	"time"

	"github.com/PRO-Robotech/kacho-compute/internal/domain"
	"github.com/PRO-Robotech/kacho-compute/internal/service"
)

// InstanceHandler обрабатывает переходы состояний Instance.
type InstanceHandler struct {
	instanceRepo service.InstanceRepo
	diskRepo     service.DiskRepo
	simCfg       SimConfig
}

// NewInstanceHandler создаёт InstanceHandler.
func NewInstanceHandler(instanceRepo service.InstanceRepo, diskRepo service.DiskRepo, simCfg SimConfig) *InstanceHandler {
	return &InstanceHandler{
		instanceRepo: instanceRepo,
		diskRepo:     diskRepo,
		simCfg:       simCfg,
	}
}

// Process обрабатывает один Instance.
func (h *InstanceHandler) Process(ctx context.Context, inst *domain.Instance) {
	// Finalizer: deletionTimestamp + disk-detach finalizer
	if inst.DeletionTimestamp != nil && containsString(inst.Finalizers, "compute.kacho.io/disk-detach") {
		h.processFinalizerDiskDetach(ctx, inst)
		return
	}

	// Restart: restartedAt > lastRestartCompletedAt
	if inst.RestartedAt != nil {
		if inst.LastRestartCompletedAt == nil || inst.RestartedAt.After(*inst.LastRestartCompletedAt) {
			if inst.State == domain.InstanceStateRunning {
				h.processRestart(ctx, inst)
				return
			}
		}
	}

	switch inst.State {
	case domain.InstanceStateProvisioning:
		h.processProvisioning(ctx, inst)
	case domain.InstanceStateStopping:
		h.processStopping(ctx, inst)
	case domain.InstanceStateStarting:
		h.processStarting(ctx, inst)
	case domain.InstanceStateRunning:
		// Check if desiredPowerState = STOPPED
		if inst.DesiredPowerState == domain.DesiredPowerStateStopped {
			h.beginStopping(ctx, inst)
		}
	case domain.InstanceStateStopped:
		// Check if desiredPowerState = RUNNING
		if inst.DesiredPowerState == domain.DesiredPowerStateRunning {
			h.beginStarting(ctx, inst)
		}
	}
}

// processProvisioning: PROVISIONING → (диски ATTACHING → READY-attached) → RUNNING
func (h *InstanceHandler) processProvisioning(ctx context.Context, inst *domain.Instance) {
	// Фаза 1: attach диски
	allDisks := h.collectDisks(inst)

	for _, diskUID := range allDisks {
		if diskUID == "" {
			continue
		}
		disk, err := h.diskRepo.GetByUID(ctx, diskUID)
		if err != nil || disk == nil {
			continue
		}
		// Переводим диск в ATTACHING
		disk.State = domain.DiskStateAttaching
		disk.StateLastTransitionAt = time.Now().UTC()
		disk.AttachedToInstanceID = inst.UID
		_, _ = h.diskRepo.UpdateStatus(ctx, disk)
	}

	// Фаза 2: симулируем задержку provisioning
	delay := randBetween(h.simCfg.InstanceProvisionMinMs, h.simCfg.InstanceProvisionMaxMs)
	select {
	case <-ctx.Done():
		return
	case <-time.After(time.Duration(delay) * time.Millisecond):
	}

	// Фаза 3: диски → READY-attached, Instance → RUNNING
	for _, diskUID := range allDisks {
		if diskUID == "" {
			continue
		}
		disk, err := h.diskRepo.GetByUID(ctx, diskUID)
		if err != nil || disk == nil {
			continue
		}
		disk.State = domain.DiskStateReady
		disk.StateLastTransitionAt = time.Now().UTC()
		disk.AttachedToInstanceID = inst.UID
		_, _ = h.diskRepo.UpdateStatus(ctx, disk)
	}

	// Instance → RUNNING
	inst.State = domain.InstanceStateRunning
	inst.StateLastTransitionAt = time.Now().UTC()
	inst.IPs = &domain.IPs{
		Internal: assignInternalIP(inst.UID),
	}
	_, _ = h.instanceRepo.UpdateStatus(ctx, inst)
}

// processStopping: STOPPING → STOPPED
func (h *InstanceHandler) processStopping(ctx context.Context, inst *domain.Instance) {
	delay := randBetween(h.simCfg.InstanceStopMinMs, h.simCfg.InstanceStopMaxMs)
	select {
	case <-ctx.Done():
		return
	case <-time.After(time.Duration(delay) * time.Millisecond):
	}

	inst.State = domain.InstanceStateStopped
	inst.StateLastTransitionAt = time.Now().UTC()
	_, _ = h.instanceRepo.UpdateStatus(ctx, inst)
}

// processStarting: STARTING → RUNNING
func (h *InstanceHandler) processStarting(ctx context.Context, inst *domain.Instance) {
	delay := randBetween(h.simCfg.InstanceStartMinMs, h.simCfg.InstanceStartMaxMs)
	select {
	case <-ctx.Done():
		return
	case <-time.After(time.Duration(delay) * time.Millisecond):
	}

	inst.State = domain.InstanceStateRunning
	inst.StateLastTransitionAt = time.Now().UTC()
	if inst.IPs == nil {
		inst.IPs = &domain.IPs{Internal: assignInternalIP(inst.UID)}
	}
	_, _ = h.instanceRepo.UpdateStatus(ctx, inst)
}

// beginStopping: RUNNING → STOPPING (немедленно, потом reconciler доделает)
func (h *InstanceHandler) beginStopping(ctx context.Context, inst *domain.Instance) {
	inst.State = domain.InstanceStateStopping
	inst.StateLastTransitionAt = time.Now().UTC()
	_, _ = h.instanceRepo.UpdateStatus(ctx, inst)
}

// beginStarting: STOPPED → STARTING
func (h *InstanceHandler) beginStarting(ctx context.Context, inst *domain.Instance) {
	inst.State = domain.InstanceStateStarting
	inst.StateLastTransitionAt = time.Now().UTC()
	_, _ = h.instanceRepo.UpdateStatus(ctx, inst)
}

// processRestart: Restart — stop + start цикл
func (h *InstanceHandler) processRestart(ctx context.Context, inst *domain.Instance) {
	restartedAt := inst.RestartedAt

	// Step 1: RUNNING → STOPPING
	inst.State = domain.InstanceStateStopping
	inst.StateLastTransitionAt = time.Now().UTC()
	_, _ = h.instanceRepo.UpdateStatus(ctx, inst)

	// Step 2: задержка STOPPING
	delay := randBetween(h.simCfg.InstanceStopMinMs, h.simCfg.InstanceStopMaxMs)
	select {
	case <-ctx.Done():
		return
	case <-time.After(time.Duration(delay) * time.Millisecond):
	}

	// Step 3: STOPPING → STOPPED
	inst.State = domain.InstanceStateStopped
	inst.StateLastTransitionAt = time.Now().UTC()
	_, _ = h.instanceRepo.UpdateStatus(ctx, inst)

	// Step 4: STOPPED → STARTING
	inst.State = domain.InstanceStateStarting
	inst.StateLastTransitionAt = time.Now().UTC()
	_, _ = h.instanceRepo.UpdateStatus(ctx, inst)

	// Step 5: задержка STARTING
	delay = randBetween(h.simCfg.InstanceStartMinMs, h.simCfg.InstanceStartMaxMs)
	select {
	case <-ctx.Done():
		return
	case <-time.After(time.Duration(delay) * time.Millisecond):
	}

	// Step 6: STARTING → RUNNING, lastRestartCompletedAt = restartedAt
	inst.State = domain.InstanceStateRunning
	inst.StateLastTransitionAt = time.Now().UTC()
	inst.LastRestartCompletedAt = restartedAt
	_, _ = h.instanceRepo.UpdateStatus(ctx, inst)
}

// processFinalizerDiskDetach: безопасное удаление с отключением дисков.
func (h *InstanceHandler) processFinalizerDiskDetach(ctx context.Context, inst *domain.Instance) {
	// Получаем все прикреплённые диски
	attachedDisks, err := h.diskRepo.ListAttachedToInstance(ctx, inst.UID)
	if err != nil {
		return
	}

	// Детачим каждый диск
	for _, disk := range attachedDisks {
		disk.State = domain.DiskStateReady
		disk.StateLastTransitionAt = time.Now().UTC()
		disk.AttachedToInstanceID = ""
		disk.DeviceName = ""
		_, _ = h.diskRepo.UpdateStatus(ctx, disk)
	}

	// Убираем finalizer compute.kacho.io/disk-detach
	newFinalizers := removeString(inst.Finalizers, "compute.kacho.io/disk-detach")

	restartedAtStr := (*string)(nil)
	_, _ = h.instanceRepo.UpdateMetadata(ctx, inst.UID, newFinalizers, true, restartedAtStr)

	// Если finalizers пустые — физически удаляем Instance
	if len(newFinalizers) == 0 {
		_ = h.instanceRepo.HardDelete(ctx, inst.UID)
	}
}

func (h *InstanceHandler) collectDisks(inst *domain.Instance) []string {
	var uids []string
	if inst.BootDisk != nil && inst.BootDisk.DiskID != "" {
		uids = append(uids, inst.BootDisk.DiskID)
	}
	for _, sd := range inst.SecondaryDisks {
		if sd.DiskID != "" {
			uids = append(uids, sd.DiskID)
		}
	}
	return uids
}

// assignInternalIP генерирует детерминированный псевдо-IP на основе UID.
// OQ-4: 10.0.0.<hash(uid) % 250 + 2>
func assignInternalIP(uid string) string {
	h := fnv32(uid)
	last := int(h%250) + 2
	ip := net.IP{10, 0, 0, byte(last)}
	return ip.String()
}

func fnv32(s string) uint32 {
	b := []byte(s)
	var h uint32 = 2166136261
	for _, c := range b {
		h ^= uint32(c)
		h *= 16777619
	}
	return h
}

// containsString проверяет наличие строки в слайсе.
func containsString(slice []string, s string) bool {
	for _, v := range slice {
		if v == s {
			return true
		}
	}
	return false
}

// removeString удаляет строку из слайса.
func removeString(slice []string, s string) []string {
	var result []string
	for _, v := range slice {
		if v != s {
			result = append(result, v)
		}
	}
	return result
}

// randBetween возвращает случайное число в диапазоне [min, max].
func randBetween(minVal, maxVal int) int {
	if minVal >= maxVal {
		return minVal
	}
	return minVal + rand.Intn(maxVal-minVal+1)
}

