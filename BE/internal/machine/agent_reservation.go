package machine

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/containerd/errdefs"
	"github.com/google/uuid"
	"github.com/ku/ku-backend/internal/domain"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

const repositoryReservationDeadlineLabel = "com.ku.repository-reservation-deadline"
const repositoryReservationVersionLabel = "com.ku.repository-reservation-version"
const repositoryReservationRecoveryGrace = 30 * time.Second

func repositoryReservationRun(options client.ContainerCreateOptions) (uuid.UUID, bool) {
	if options.Config == nil {
		return uuid.Nil, false
	}
	key := "com.ku.source-sync-run"
	if verificationService(options.Config.Labels["com.ku.service"]) {
		key = "com.ku.agent-run-id"
	}
	id, err := uuid.Parse(options.Config.Labels[key])
	return id, err == nil && id != uuid.Nil && id.String() == options.Config.Labels[key]
}

// Local callers retain their reservation through attestation and cleanup,
// including after the Docker process exits. A new backend can reclaim an
// expired terminal reservation by immutable ID; running writers stay intact.
func (r *DockerRuntime) acquireRepositoryReservation(ctx context.Context, machine *domain.DevMachine, options *client.ContainerCreateOptions) (func(), error) {
	runID, reserved := repositoryReservationRun(*options)
	if !reserved {
		if options.Config != nil && (verificationService(options.Config.Labels["com.ku.service"]) || options.Config.Labels["com.ku.source-sync-run"] != "") {
			return nil, errors.New("repository reservation run authority is invalid")
		}
		return func() {}, nil
	}
	deadline, bounded := ctx.Deadline()
	if !bounded {
		return nil, errors.New("repository reservation requires a deadline")
	}
	r.reservationMu.Lock()
	if _, active := r.activeReservations[options.Name]; active {
		r.reservationMu.Unlock()
		return nil, errors.New("repository reservation is held by an active invocation")
	}
	if r.activeReservations == nil {
		r.activeReservations = make(map[string]struct{})
	}
	r.activeReservations[options.Name] = struct{}{}
	r.reservationMu.Unlock()
	release := sync.OnceFunc(func() {
		r.reservationMu.Lock()
		delete(r.activeReservations, options.Name)
		r.reservationMu.Unlock()
	})
	options.Config.Labels[repositoryReservationVersionLabel] = "1"
	options.Config.Labels[repositoryReservationDeadlineLabel] = strconv.FormatInt(deadline.UnixNano(), 10)
	inspection, err := r.client.ContainerInspect(ctx, options.Name, client.ContainerInspectOptions{})
	if errdefs.IsNotFound(err) {
		return release, nil
	}
	if err != nil {
		release()
		return nil, err
	}
	old := inspection.Container
	if err := validateExpiredRepositoryReservation(machine, *options, runID, old, time.Now()); err != nil {
		release()
		return nil, err
	}
	// Without Force, a start racing this inspection cannot turn an active
	// container into a deletion. Never delete by reusable name or touch data.
	_, err = r.client.ContainerRemove(ctx, old.ID, client.ContainerRemoveOptions{})
	if err != nil && !errdefs.IsNotFound(err) {
		release()
		return nil, err
	}
	return release, nil
}

func validateExpiredRepositoryReservation(machine *domain.DevMachine, wanted client.ContainerCreateOptions, runID uuid.UUID, old container.InspectResponse, now time.Time) error {
	config, host, state := old.Config, old.HostConfig, old.State
	if machine == nil || wanted.Config == nil || wanted.HostConfig == nil || config == nil || host == nil || state == nil || old.ID == "" || old.Name != "/"+wanted.Name || !ownedByMachine(config.Labels, machine.ID) || config.Labels["com.ku.workspace-id"] != machine.WorkspaceID.String() || config.Labels["com.ku.routing-key"] != machine.RoutingKey {
		return errors.New("repository reservation recovery ownership is unavailable")
	}
	operation, err := uuid.Parse(config.Labels["com.ku.repository-operation"])
	if err != nil || operation == uuid.Nil || operation.String() != config.Labels["com.ku.repository-operation"] || config.Labels[repositoryReservationVersionLabel] != "1" {
		return errors.New("repository reservation recovery authority is invalid")
	}
	oldRun, validRun := repositoryReservationRun(client.ContainerCreateOptions{Config: config})
	if !validRun || oldRun != runID || config.Labels["com.ku.service"] != wanted.Config.Labels["com.ku.service"] || config.Labels["com.ku.verification-commit"] != wanted.Config.Labels["com.ku.verification-commit"] || config.Image != wanted.Config.Image || !isImmutableLocalImageID(config.Image) || config.User != "1000:1000" || config.WorkingDir != wanted.Config.WorkingDir {
		return errors.New("repository reservation recovery does not match its run or immutable image")
	}
	if !host.ReadonlyRootfs || host.Privileged || len(host.Binds) != 0 || len(host.VolumesFrom) != 0 || len(host.CapAdd) != 0 || host.NetworkMode != wanted.HostConfig.NetworkMode || !reflect.DeepEqual(host.Tmpfs, wanted.HostConfig.Tmpfs) || !reflect.DeepEqual(host.Mounts, wanted.HostConfig.Mounts) || !reflect.DeepEqual(host.CapDrop, wanted.HostConfig.CapDrop) || !reflect.DeepEqual(host.SecurityOpt, wanted.HostConfig.SecurityOpt) {
		return errors.New("repository reservation recovery mount or privilege policy changed")
	}
	deadline, err := strconv.ParseInt(config.Labels[repositoryReservationDeadlineLabel], 10, 64)
	if err != nil || deadline <= 0 || now.Before(time.Unix(0, deadline).Add(repositoryReservationRecoveryGrace)) {
		return errors.New("repository reservation has not expired")
	}
	if state.Running || state.Paused || state.Restarting || state.Status != container.StateExited && state.Status != container.StateCreated {
		return errors.New("repository reservation still has an active or ambiguous process")
	}
	return nil
}

func verificationReservationOwner(inspection container.InspectResponse, operationID string) (string, error) {
	if inspection.Config == nil || inspection.Config.Labels["com.ku.repository-operation"] != operationID || inspection.ID == "" {
		return "", errors.New("verification reservation ownership changed before preparation")
	}
	created, err := time.Parse(time.RFC3339Nano, inspection.Created)
	if err != nil || created.UnixNano() <= 0 {
		return "", errors.New("verification reservation creation authority is unavailable")
	}
	if _, err := uuid.Parse(operationID); err != nil || strings.ContainsAny(operationID, ":\r\n") {
		return "", errors.New("verification reservation operation is invalid")
	}
	return strconv.FormatInt(created.UnixNano(), 10) + ":" + operationID, nil
}
