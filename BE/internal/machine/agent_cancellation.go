package machine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/containerd/errdefs"
	"github.com/google/uuid"
	"github.com/ku/ku-backend/internal/domain"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

// A stopped, mountless Docker record survives a backend restart. It is kept
// until machine teardown so even a delayed create response cannot start a new
// execution after cancellation was acknowledged. Cleanup does not publish or
// execute provider/test code and may still remove the run's private files.
func agentCancellationName(machine *domain.DevMachine, runID uuid.UUID) string {
	return "ku-" + machine.RoutingKey + "-cancel-" + runID.String()
}

func validateAgentCancellation(inspection container.InspectResponse, machine *domain.DevMachine, runID uuid.UUID) error {
	config, host, state := inspection.Config, inspection.HostConfig, inspection.State
	if machine == nil || runID == uuid.Nil || config == nil || host == nil || state == nil || inspection.ID == "" || inspection.Name != "/"+agentCancellationName(machine, runID) || !ownedByMachine(config.Labels, machine.ID) || config.Labels["com.ku.workspace-id"] != machine.WorkspaceID.String() || config.Labels["com.ku.routing-key"] != machine.RoutingKey || config.Labels["com.ku.agent-run-id"] != runID.String() || config.Labels["com.ku.service"] != "agent-cancellation" || config.Labels["com.ku.cancellation-version"] != "1" {
		return errors.New("agent cancellation record ownership is invalid")
	}
	if !isImmutableLocalImageID(config.Image) || config.User != "1000:1000" || len(host.Mounts) != 0 || len(host.Binds) != 0 || len(host.VolumesFrom) != 0 || len(config.Volumes) != 0 || host.Privileged || !host.ReadonlyRootfs || host.NetworkMode != "none" || len(host.CapAdd) != 0 || state.Running || state.Paused || state.Restarting || state.Status != container.StateCreated && state.Status != container.StateExited {
		return errors.New("agent cancellation record isolation is invalid")
	}
	return nil
}

func (r *DockerRuntime) requireAgentRunActive(ctx context.Context, machine *domain.DevMachine, runID uuid.UUID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if runID == uuid.Nil {
		return nil
	}
	if machine == nil || machine.ID == uuid.Nil || machine.WorkspaceID == uuid.Nil || machine.RoutingKey == "" {
		return errors.New("agent run machine authority is unavailable")
	}
	inspected, err := r.client.ContainerInspect(ctx, agentCancellationName(machine, runID), client.ContainerInspectOptions{})
	if errdefs.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := validateAgentCancellation(inspected.Container, machine, runID); err != nil {
		return err
	}
	return fmt.Errorf("agent run was cancelled: %w", context.Canceled)
}

func (r *DockerRuntime) recordAgentCancellation(ctx context.Context, machine *domain.DevMachine, runID uuid.UUID) error {
	name := agentCancellationName(machine, runID)
	inspected, err := r.client.ContainerInspect(ctx, name, client.ContainerInspectOptions{})
	if err == nil {
		return validateAgentCancellation(inspected.Container, machine, runID)
	}
	if !errdefs.IsNotFound(err) {
		return err
	}
	// An unprovisioned queued run has no volume, layout or executable container.
	// The database's terminal status prevents provisioning this cancelled run.
	if machine.WorkspaceVolumeName == nil || *machine.WorkspaceVolumeName == "" {
		return nil
	}
	layout, err := r.client.ContainerInspect(ctx, workspaceLayoutName(machine), client.ContainerInspectOptions{})
	if err != nil {
		return err
	}
	if _, err = validateWorkspaceLayoutRecord(layout.Container, machine, *machine.WorkspaceVolumeName); err != nil {
		return err
	}
	if !isImmutableLocalImageID(layout.Container.Config.Image) {
		return errors.New("cancellation record requires the protected layout's immutable image")
	}
	labels := machineLabels(machine)
	labels["com.ku.service"], labels["com.ku.cancellation-version"], labels["com.ku.agent-run-id"] = "agent-cancellation", "1", runID.String()
	_, createErr := r.client.ContainerCreate(ctx, client.ContainerCreateOptions{
		Name: name, Config: &container.Config{Image: layout.Container.Config.Image, User: "1000:1000", Entrypoint: []string{"/bin/true"}, Labels: labels},
		HostConfig: &container.HostConfig{NetworkMode: "none", ReadonlyRootfs: true, CapDrop: []string{"ALL"}, SecurityOpt: []string{"no-new-privileges=true"}, Resources: container.Resources{Memory: 16 * 1024 * 1024, MemorySwap: 16 * 1024 * 1024, NanoCPUs: 100_000_000, PidsLimit: new(int64(8))}},
	})
	// Inspect even after a conflict/lost response. Accept only our exact record;
	// never adopt, delete or start a foreign same-name container.
	inspected, err = r.client.ContainerInspect(ctx, name, client.ContainerInspectOptions{})
	if err != nil {
		return errors.Join(createErr, err)
	}
	return validateAgentCancellation(inspected.Container, machine, runID)
}

func validateAgentCancellationTarget(inspection container.InspectResponse, machine *domain.DevMachine, runID uuid.UUID) error {
	config := inspection.Config
	if config == nil || inspection.ID == "" || !ownedByMachine(config.Labels, machine.ID) || config.Labels["com.ku.workspace-id"] != machine.WorkspaceID.String() || config.Labels["com.ku.routing-key"] != machine.RoutingKey || config.Labels["com.ku.agent-run-id"] != runID.String() && config.Labels["com.ku.source-sync-run"] != runID.String() {
		return errors.New("agent cancellation target ownership is invalid")
	}
	for _, key := range []string{"com.ku.agent-run-id", "com.ku.source-sync-run"} {
		if value := config.Labels[key]; value != "" && value != runID.String() {
			return errors.New("agent cancellation target has conflicting run authority")
		}
	}
	prefix := "ku-" + machine.RoutingKey
	var wanted string
	switch config.Labels["com.ku.service"] {
	case "agent":
		wanted = prefix + "-agent-" + runID.String()
	case "git-verification", "workspace-verification":
		operation, err := uuid.Parse(config.Labels["com.ku.repository-operation"])
		if err != nil || operation == uuid.Nil || operation.String() != config.Labels["com.ku.repository-operation"] || !isImmutableLocalImageID(config.Image) {
			return errors.New("agent cancellation verification authority is invalid")
		}
		wanted = prefix + "-verification-" + runID.String()
	case "git-helper", "git-checkout":
		operation, err := uuid.Parse(config.Labels["com.ku.repository-operation"])
		if err != nil || operation == uuid.Nil || operation.String() != config.Labels["com.ku.repository-operation"] || !isImmutableLocalImageID(config.Image) {
			return errors.New("agent cancellation helper authority is invalid")
		}
		wanted = prefix + "-git-" + operation.String()
		if config.Labels["com.ku.source-sync-run"] != "" {
			if config.Labels["com.ku.source-sync-run"] != runID.String() {
				return errors.New("source synchronization run authority changed")
			}
			wanted = prefix + "-source-sync-" + runID.String()
		}
	default:
		return errors.New("agent cancellation target service is invalid")
	}
	if inspection.Name != "/"+wanted {
		return errors.New("agent cancellation target name changed")
	}
	return nil
}

func (r *DockerRuntime) CancelAgent(ctx context.Context, machine *domain.DevMachine, run *domain.DevMachineAgentRun) error {
	if machine == nil || run == nil || machine.ID == uuid.Nil || machine.WorkspaceID == uuid.Nil || machine.RoutingKey == "" || run.ID == uuid.Nil || run.MachineID != machine.ID || run.WorkspaceID != machine.WorkspaceID {
		return errors.New("agent cancellation requires the exact machine, workspace and run")
	}
	// Establish the fence before listing so a racing create must either be in
	// this list or see the fence in its post-create check before starting.
	returnedErr := r.recordAgentCancellation(ctx, machine, run.ID)
	listed, err := r.client.ContainerList(ctx, client.ContainerListOptions{All: true})
	if err != nil {
		return errors.Join(returnedErr, err)
	}
	for _, item := range listed.Items {
		if item.Labels["com.ku.agent-run-id"] != run.ID.String() && item.Labels["com.ku.source-sync-run"] != run.ID.String() {
			continue
		}
		if item.Labels["com.ku.service"] == "agent-cancellation" || item.Labels["com.ku.service"] == "git-cleanup" {
			continue
		}
		inspection, err := r.client.ContainerInspect(ctx, item.ID, client.ContainerInspectOptions{})
		if errdefs.IsNotFound(err) {
			continue
		}
		if err != nil {
			returnedErr = errors.Join(returnedErr, err)
			continue
		}
		if inspection.Container.ID != item.ID {
			returnedErr = errors.Join(returnedErr, errors.New("agent cancellation target identity changed"))
			continue
		}
		if err = validateAgentCancellationTarget(inspection.Container, machine, run.ID); err != nil {
			returnedErr = errors.Join(returnedErr, fmt.Errorf("container %s: %w", strings.ToValidUTF8(item.ID, "?"), err))
			continue
		}
		_, err = r.client.ContainerRemove(ctx, item.ID, client.ContainerRemoveOptions{Force: true})
		if err != nil && !errdefs.IsNotFound(err) {
			returnedErr = errors.Join(returnedErr, err)
			continue
		}
		if err := r.waitAgentContainerRemoved(ctx, item.ID); err != nil {
			returnedErr = errors.Join(returnedErr, err)
		}
	}
	return returnedErr
}

// A successful remove response alone does not settle StateRemoving. Recovery
// must wait for immutable-ID disappearance before reclaiming mounted data.
func (r *DockerRuntime) waitAgentContainerRemoved(ctx context.Context, containerID string) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	for {
		_, err := r.client.ContainerInspect(ctx, containerID, client.ContainerInspectOptions{})
		if errdefs.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}
		timer := time.NewTimer(25 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
