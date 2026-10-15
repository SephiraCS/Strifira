package machine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/SephiraCS/strifira-backend/internal/domain"
	"github.com/containerd/errdefs"
	"github.com/google/uuid"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
)

const repositoryHelperTimeout = 5 * time.Minute

type repositoryHelperInvocation struct {
	RunID       uuid.UUID
	Checkout    bool
	SourceSync  bool
	Cleanup     bool
	Recovery    bool
	ProvisionID uuid.UUID
}

// The helper is the only writer of the protected repository subdirectory. It
// never inherits an IDE image, environment, executable, or writable rootfs.
func repositoryHelperOptions(machine *domain.DevMachine, imageID, operationID string, command []string) (client.ContainerCreateOptions, error) {
	if machine == nil || machine.WorkspaceVolumeName == nil || *machine.WorkspaceVolumeName == "" || machine.DockerNetworkName == nil || *machine.DockerNetworkName == "" {
		return client.ContainerCreateOptions{}, errors.New("repository helper requires machine volume and network")
	}
	if !isImmutableLocalImageID(imageID) || len(command) == 0 || machine.MemoryMB <= 0 || machine.CPUMillis <= 0 {
		return client.ContainerCreateOptions{}, errors.New("repository helper requires an immutable image, command and resource budget")
	}
	labels := machineLabels(machine)
	labels["com.strifira.service"] = "git-helper"
	labels["com.strifira.repository-operation"] = operationID
	memory := min(int64(512), max(int64(128), int64(machine.MemoryMB)*15/100)) * 1024 * 1024
	return client.ContainerCreateOptions{
		Name: "strifira-" + machine.RoutingKey + "-git-" + operationID,
		Config: &container.Config{
			Image: imageID, User: "1000:1000", WorkingDir: "/repositories",
			Entrypoint: command, Cmd: []string{}, Env: []string{"PATH=/usr/bin:/bin", "HOME=/tmp"},
			Labels: labels, OpenStdin: true, StdinOnce: true, AttachStdin: true, AttachStdout: true, AttachStderr: true,
		},
		HostConfig: &container.HostConfig{
			NetworkMode: container.NetworkMode(*machine.DockerNetworkName), ReadonlyRootfs: true,
			CapDrop: []string{"ALL"}, SecurityOpt: []string{"no-new-privileges=true"},
			LogConfig: container.LogConfig{Type: "none"},
			Tmpfs:     map[string]string{"/tmp": "rw,noexec,nosuid,nodev,uid=1000,gid=1000,mode=0700,size=16m"},
			Mounts: []mount.Mount{
				{Type: mount.TypeVolume, Source: *machine.WorkspaceVolumeName, Target: "/workspace", ReadOnly: true,
					VolumeOptions: &mount.VolumeOptions{NoCopy: true, Subpath: "workspace"}},
				{Type: mount.TypeVolume, Source: *machine.WorkspaceVolumeName, Target: "/repositories",
					VolumeOptions: &mount.VolumeOptions{NoCopy: true, Subpath: "repositories"}},
			},
			Resources: container.Resources{
				Memory: memory, MemorySwap: memory,
				NanoCPUs:  min(int64(1_000_000_000), max(int64(100_000_000), int64(machine.CPUMillis)*1_000_000/10)),
				PidsLimit: new(int64(64)),
			},
		},
		NetworkingConfig: &network.NetworkingConfig{EndpointsConfig: map[string]*network.EndpointSettings{*machine.DockerNetworkName: {}}},
	}, nil
}

// Refuse even stopped legacy containers: restarting a whole-volume writer
// would make the protected store agent-writable. Layout migration must remove
// these containers before enabling this path. Never mutate them here.
func (r *DockerRuntime) requireProtectedRepositoryMounts(ctx context.Context, machine *domain.DevMachine, imageID string) error {
	volumeName := *machine.WorkspaceVolumeName
	listed, err := r.client.ContainerList(ctx, client.ContainerListOptions{All: true})
	if err != nil {
		return err
	}
	for _, item := range listed.Items {
		usesVolume := false
		for _, mounted := range item.Mounts {
			if mounted.Name == volumeName && mounted.RW {
				usesVolume = true
				break
			}
		}
		if !usesVolume {
			continue
		}
		inspection, err := r.client.ContainerInspect(ctx, item.ID, client.ContainerInspectOptions{})
		if errdefs.IsNotFound(err) {
			continue
		}
		if err != nil {
			return err
		}
		config := inspection.Container.Config
		if config == nil {
			return errors.New("workspace volume consumer configuration is unavailable")
		}
		if protectedVerificationMounts(inspection.Container, machine) || protectedWorkspaceVerificationMounts(inspection.Container, machine) {
			continue
		}
		operationID, operationErr := uuid.Parse(config.Labels["com.strifira.repository-operation"])
		trustedHelper := ownedByMachine(config.Labels, machine.ID) && (config.Labels["com.strifira.service"] == "git-helper" || config.Labels["com.strifira.service"] == "git-cleanup") &&
			operationErr == nil && operationID != uuid.Nil && config.Image == imageID && config.User == "1000:1000"
		if !protectedRepositoryMounts(inspection.Container.HostConfig, volumeName, trustedHelper) && !protectedAtomicWriterMounts(inspection.Container, volumeName, trustedHelper) {
			return errors.New("machine has a writable legacy repository mount; stop and migrate its services first")
		}
	}
	return nil
}

func protectedRepositoryMounts(config *container.HostConfig, volumeName string, trustedHelper bool) bool {
	if config == nil {
		return false
	}
	// Bind strings have no subpath policy. Reject them conservatively instead
	// of relying on path aliases or the container's claim of its service type.
	if len(config.Binds) != 0 {
		return false
	}
	found := false
	for _, mounted := range config.Mounts {
		if mounted.Source != volumeName || mounted.Type != mount.TypeVolume {
			continue
		}
		found = true
		if mounted.ReadOnly {
			continue
		}
		if mounted.VolumeOptions == nil {
			return false
		}
		if mounted.VolumeOptions.Subpath != "workspace" && !(trustedHelper && mounted.VolumeOptions.Subpath == "repositories" && mounted.Target == "/repositories") {
			return false
		}
	}
	return found
}

// Only an immutable backend helper with exact machine/layout ownership may
// see the common volume mount. UID1000 cannot modify the root-owned layout
// record or volume root; its authority equals its two writable subpaths.
func protectedAtomicWriterMounts(item container.InspectResponse, volumeName string, trustedHelper bool) bool {
	if !trustedHelper || item.Config == nil || item.HostConfig == nil || item.Config.User != "1000:1000" || item.Config.Labels["com.strifira.service"] != "git-helper" || len(item.HostConfig.Mounts) != 3 || len(item.HostConfig.Binds) != 0 || !item.HostConfig.ReadonlyRootfs || item.HostConfig.Privileged || item.HostConfig.NetworkMode != "none" {
		return false
	}
	if len(item.HostConfig.CapAdd) != 0 || len(item.HostConfig.CapDrop) != 1 || item.HostConfig.CapDrop[0] != "ALL" || len(item.HostConfig.SecurityOpt) != 1 || item.HostConfig.SecurityOpt[0] != "no-new-privileges=true" {
		return false
	}
	identity := item.Config.Labels["com.strifira.source-sync-run"]
	if identity == "" {
		identity = item.Config.Labels["com.strifira.provision-checkout"]
	}
	parsed, err := uuid.Parse(identity)
	if err != nil || parsed == uuid.Nil || parsed.String() != identity {
		return false
	}
	for i, expected := range []struct{ target, subpath string }{{"/workspace", "workspace"}, {"/repositories", "repositories"}, {"/volume", ""}} {
		mounted := item.HostConfig.Mounts[i]
		if mounted.Type != mount.TypeVolume || mounted.Source != volumeName || mounted.Target != expected.target || mounted.ReadOnly || mounted.VolumeOptions == nil || !mounted.VolumeOptions.NoCopy || mounted.VolumeOptions.Subpath != expected.subpath {
			return false
		}
	}
	return true
}

// Credentials are sent only over the attached stdin, after creation. Cancellation
// removes the entire one-shot container, including descendants; attachment EOF
// alone can never report success. A retained candidate survives helper cleanup.
func (r *DockerRuntime) runRepositoryHelper(ctx context.Context, machine *domain.DevMachine, services []domain.DevMachineService, command []string, credential string, invocation repositoryHelperInvocation) (output string, returnedErr error) {
	return r.runImmutableGitHelper(ctx, machine, services, command, credential, invocation)
}

func (r *DockerRuntime) runCheckoutHelper(ctx context.Context, machine *domain.DevMachine, services []domain.DevMachineService, command []string) (string, error) {
	return r.runImmutableGitHelper(ctx, machine, services, command, "", repositoryHelperInvocation{Checkout: true})
}

func (r *DockerRuntime) runImmutableGitHelper(ctx context.Context, machine *domain.DevMachine, services []domain.DevMachineService, command []string, credential string, invocation repositoryHelperInvocation) (output string, returnedErr error) {
	ctx, cancel := context.WithTimeout(ctx, repositoryHelperTimeout)
	defer cancel()
	if strings.ContainsAny(credential, "\r\n\x00") {
		return "", errors.New("repository credential contains an invalid record separator")
	}
	imageID, err := r.repositoryHelperImage(ctx, services)
	if err != nil {
		return "", err
	}
	operationID := uuid.NewString()
	options, err := repositoryHelperOptions(machine, imageID, operationID, command)
	if err != nil {
		return "", err
	}
	if invocation.RunID != uuid.Nil {
		options.Config.Labels["com.strifira.agent-run-id"] = invocation.RunID.String()
	}
	if invocation.Recovery && (!invocation.Cleanup || !invocation.Checkout || !invocation.SourceSync) {
		return "", errors.New("terminal recovery requires a cleanup source synchronization helper")
	}
	if invocation.Cleanup && (invocation.RunID == uuid.Nil || !invocation.Recovery && (invocation.Checkout || invocation.SourceSync) || credential != "") {
		return "", errors.New("repository cleanup requires a credential-free agent run helper")
	}
	if invocation.Recovery {
		inspection, err := r.client.ContainerInspect(ctx, agentCancellationName(machine, invocation.RunID), client.ContainerInspectOptions{})
		if err != nil {
			return "", err
		}
		if err := validateAgentCancellation(inspection.Container, machine, invocation.RunID); err != nil {
			return "", err
		}
	}
	if invocation.SourceSync {
		if !invocation.Checkout || invocation.RunID == uuid.Nil || credential != "" {
			return "", errors.New("source synchronization requires a credential-free checkout helper")
		}
		options.Name = "strifira-" + machine.RoutingKey + "-source-sync-" + invocation.RunID.String()
		options.Config.Labels["com.strifira.source-sync-run"] = invocation.RunID.String()
	}
	if invocation.ProvisionID != uuid.Nil {
		if !invocation.Checkout || invocation.RunID != uuid.Nil || invocation.SourceSync || invocation.Cleanup || invocation.Recovery || credential != "" {
			return "", errors.New("default provisioning requires an isolated credential-free checkout helper")
		}
		options.Name = "strifira-" + machine.RoutingKey + "-provision-" + invocation.ProvisionID.String()
		options.Config.Labels["com.strifira.provision-checkout"] = invocation.ProvisionID.String()
	}
	if invocation.Checkout {
		options.Config.Labels["com.strifira.service"] = "git-checkout"
		options.HostConfig.Mounts[0].ReadOnly = false
		options.HostConfig.Mounts[1].ReadOnly = true
		options.HostConfig.NetworkMode = "none"
		options.NetworkingConfig = nil
	}
	if invocation.SourceSync || invocation.ProvisionID != uuid.Nil {
		// This immutable, network-isolated helper alone may update both the
		// editable Git metadata and its protected recovery transaction.
		options.Config.Labels["com.strifira.service"] = "git-helper"
		options.HostConfig.Mounts[1].ReadOnly = false
		options.HostConfig.Mounts = append(options.HostConfig.Mounts, mount.Mount{Type: mount.TypeVolume, Source: *machine.WorkspaceVolumeName, Target: "/volume", VolumeOptions: &mount.VolumeOptions{NoCopy: true}})
	}
	if invocation.Cleanup {
		options.Config.Labels["com.strifira.service"] = "git-cleanup"
		options.HostConfig.NetworkMode = "none"
		options.NetworkingConfig = nil
	}
	if err := r.verifyRepositoryHelperWorkspace(ctx, machine, imageID); err != nil {
		return "", err
	}
	execution, err := r.executeRepositoryContainer(ctx, machine, options, credential, repositoryContainerHooks{})
	if err != nil {
		return "", err
	}
	if execution.ExitCode != 0 {
		diagnostic := strings.TrimSpace(execution.Stderr)
		if diagnostic == "" {
			diagnostic = strings.TrimSpace(execution.Stdout)
		}
		diagnostic = strings.ToValidUTF8(strings.ReplaceAll(diagnostic, "\x00", ""), "\uFFFD")
		return "", fmt.Errorf("repository helper exited %d: %s", execution.ExitCode, diagnostic)
	}
	return execution.Stdout, nil
}

type repositoryContainerHooks struct {
	afterCreate  func(context.Context, string) error
	beforeStart  func(context.Context) error
	afterExit    func(context.Context, *CommandExecution) error
	beforeRemove func(context.Context, string) error
}

// One lifecycle handles helper and test containers. A confirmed process exit,
// owned cleanup and cancellation must all agree before callers can trust output.
func (r *DockerRuntime) executeRepositoryContainer(ctx context.Context, machine *domain.DevMachine, options client.ContainerCreateOptions, credential string, hooks repositoryContainerHooks) (execution *CommandExecution, returnedErr error) {
	if machine == nil || options.Config == nil || options.HostConfig == nil || options.Name == "" || strings.ContainsAny(credential, "\r\n\x00") {
		return nil, errors.New("repository container options or credential are invalid")
	}
	operationID := options.Config.Labels["com.strifira.repository-operation"]
	operation, err := uuid.Parse(operationID)
	if err != nil || operation == uuid.Nil || !ownedByMachine(options.Config.Labels, machine.ID) {
		return nil, errors.New("repository container ownership is invalid")
	}
	runID, _ := uuid.Parse(options.Config.Labels["com.strifira.agent-run-id"])
	checkActive := func() error {
		if options.Config.Labels["com.strifira.service"] == "git-cleanup" {
			return nil
		}
		return r.requireAgentRunActive(ctx, machine, runID)
	}
	if err := checkActive(); err != nil {
		return nil, err
	}
	release, err := r.acquireRepositoryReservation(ctx, machine, &options)
	if err != nil {
		return nil, err
	}
	defer release()
	containerID := ""
	remove := sync.OnceValue(func() error {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		if containerID == "" {
			// A lost create response can leave a container under our unique name.
			// Recover only the invocation's exact ownership before removing it.
			inspection, inspectErr := r.client.ContainerInspect(cleanupCtx, options.Name, client.ContainerInspectOptions{})
			if errdefs.IsNotFound(inspectErr) {
				return nil
			}
			if inspectErr != nil {
				return inspectErr
			}
			if inspection.Container.Config == nil {
				return errors.New("repository helper cleanup ownership is unavailable")
			}
			labels := inspection.Container.Config.Labels
			if !ownedByMachine(labels, machine.ID) || labels["com.strifira.repository-operation"] != operationID || labels["com.strifira.service"] != options.Config.Labels["com.strifira.service"] {
				return errors.New("repository helper cleanup ownership does not match")
			}
			containerID = inspection.Container.ID
		}
		if hooks.beforeRemove != nil {
			if err := hooks.beforeRemove(cleanupCtx, containerID); err != nil {
				return err
			}
		}
		_, removeErr := r.client.ContainerRemove(cleanupCtx, containerID, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true})
		if errdefs.IsNotFound(removeErr) {
			return nil
		}
		return removeErr
	})
	defer func() {
		if contextErr := ctx.Err(); contextErr != nil {
			execution = nil
			returnedErr = errors.Join(returnedErr, contextErr)
		}
		if cleanupErr := remove(); cleanupErr != nil {
			execution = nil
			returnedErr = errors.Join(returnedErr, fmt.Errorf("repository helper cleanup failed: %w", cleanupErr))
		}
		secrets := map[string]string{"GITHUB_TOKEN": credential}
		if execution != nil {
			execution.Stdout = redact(execution.Stdout, secrets)
			execution.Stderr = redact(execution.Stderr, secrets)
		}
		if returnedErr != nil {
			returnedErr = &repositoryHelperError{cause: returnedErr, message: redact(returnedErr.Error(), secrets)}
		}
	}()
	created, err := r.client.ContainerCreate(ctx, options)
	if err != nil {
		return nil, err
	}
	containerID = created.ID
	if err := checkActive(); err != nil {
		return nil, err
	}
	if hooks.afterCreate != nil {
		if err := hooks.afterCreate(ctx, containerID); err != nil {
			return nil, err
		}
	}
	attached, err := r.client.ContainerAttach(ctx, containerID, client.ContainerAttachOptions{Stream: true, Stdin: options.Config.OpenStdin, Stdout: true, Stderr: true})
	if err != nil {
		return nil, err
	}
	defer attached.Close()
	if hooks.beforeStart != nil {
		if err := hooks.beforeStart(ctx); err != nil {
			return nil, err
		}
	}
	stopCancel := context.AfterFunc(ctx, func() {
		attached.Close()
		_ = remove()
	})
	defer func() {
		if !stopCancel() {
			_ = remove()
		}
	}()
	if _, err := r.client.ContainerStart(ctx, containerID, client.ContainerStartOptions{}); err != nil {
		return nil, err
	}
	if credential != "" {
		if _, err := io.WriteString(attached.Conn, credential+"\n"); err != nil {
			return nil, err
		}
	}
	if err := attached.CloseWrite(); err != nil {
		return nil, err
	}
	stdout, stderr := newCappedBuffer(maxCapturedLogBytes), newCappedBuffer(maxCapturedLogBytes)
	if _, err := stdcopy.StdCopy(&stdout, &stderr, attached.Reader); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if (stdout.overflow || stderr.overflow) && !verificationService(options.Config.Labels["com.strifira.service"]) {
		return nil, errors.New("repository helper output exceeds the capture limit")
	}
	inspection, err := r.client.ContainerInspect(ctx, containerID, client.ContainerInspectOptions{})
	if err != nil {
		return nil, errors.Join(err, checkActive())
	}
	// External cancellation removes the Docker process independently from this
	// caller's context. Recheck its durable fence before trusting an exit status,
	// including when the exit or lost inspection races acknowledged removal.
	if err := checkActive(); err != nil {
		return nil, err
	}
	if inspection.Container.State == nil || inspection.Container.State.Running || inspection.Container.State.Status != container.StateExited {
		return nil, errors.New("repository helper attachment ended before execution completed")
	}
	execution = &CommandExecution{Stdout: stdout.String(), Stderr: stderr.String(), StdoutTruncated: stdout.overflow, StderrTruncated: stderr.overflow, ExitCode: inspection.Container.State.ExitCode}
	if hooks.afterExit != nil {
		if err := hooks.afterExit(ctx, execution); err != nil {
			return execution, err
		}
	}
	return execution, nil
}

type repositoryHelperError struct {
	cause   error
	message string
}

func (e *repositoryHelperError) Error() string { return e.message }
func (e *repositoryHelperError) Unwrap() error { return e.cause }

func (r *DockerRuntime) repositoryHelperImage(ctx context.Context, services []domain.DevMachineService) (string, error) {
	for _, service := range services {
		if service.ServiceType != "collector" || service.ImageRef == "" {
			continue
		}
		if err := r.ensureImage(ctx, service.ImageRef); err != nil {
			return "", err
		}
		image, err := r.client.ImageInspect(ctx, service.ImageRef)
		if err != nil {
			return "", err
		}
		if !isImmutableLocalImageID(image.ID) {
			return "", errors.New("immutable Git helper image identity is unavailable")
		}
		return image.ID, nil
	}
	return "", errors.New("immutable Git helper image is unavailable")
}

func (r *DockerRuntime) verifyRepositoryHelperWorkspace(ctx context.Context, machine *domain.DevMachine, imageID string) error {
	exists, err := r.inspectWorkspaceVolume(ctx, *machine.WorkspaceVolumeName, machineLabels(machine), machine.DiskGB)
	if err != nil {
		return err
	}
	if !exists {
		return errors.New("repository helper workspace volume is unavailable")
	}
	if err := r.requireProtectedRepositoryMounts(ctx, machine, imageID); err != nil {
		return err
	}
	return r.verifyProtectedWorkspaceVolume(ctx, machine, *machine.WorkspaceVolumeName, imageID)
}
