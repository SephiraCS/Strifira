package machine

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/containerd/errdefs"
	"github.com/google/uuid"
	"github.com/ku/ku-backend/internal/domain"
	"github.com/moby/moby/client"
	"github.com/stretchr/testify/require"
)

func TestWorkspaceVerificationActualDocker(t *testing.T) {
	image := os.Getenv("KU_GIT_HELPER_IMAGE")
	if image == "" {
		t.Skip("set KU_GIT_HELPER_IMAGE to the freshly built shipped collector image")
	}
	tooling := os.Getenv("KU_GIT_SERVER_FIXTURE_IMAGE")
	require.NotEmpty(t, tooling, "an immutable fixture tooling image with a custom tool and baked credentials is required")
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Minute)
	defer cancel()
	runtime, err := NewDockerRuntime(DockerConfig{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = runtime.client.Close() })
	machine := fixtureHelperMachine()
	machine.MemoryMB, machine.CPUMillis, machine.PidsLimit = 1024, 1000, 128
	machine.RoutingKey = "filesystem-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	machine.WorkspaceVolumeName = new("ku-workspace-" + machine.RoutingKey)
	machine.DockerNetworkName = new("ku-machine-" + machine.RoutingKey)
	services := []domain.DevMachineService{{ServiceType: "collector", ImageRef: image}, {ServiceType: "ide", ImageRef: tooling}}
	created, err := runtime.ensureVolume(ctx, *machine.WorkspaceVolumeName, machineLabels(machine), machine.DiskGB)
	require.NoError(t, err)
	require.True(t, created)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), time.Minute)
		defer cleanupCancel()
		require.NoError(t, runtime.Teardown(cleanupCtx, machine, nil))
	})
	require.NoError(t, runtime.prepareProtectedWorkspaceVolume(ctx, machine, *machine.WorkspaceVolumeName, image))
	created, err = runtime.ensureNetwork(ctx, *machine.DockerNetworkName, true, machineLabels(machine))
	require.NoError(t, err)
	require.True(t, created)
	seed := "set -eu; printf 'owner-original\\n' > /workspace/source.txt; printf 'hidden\\n' > /workspace/.hidden; ln /workspace/source.txt /workspace/hardlink; ln -s /repositories/backend-only /workspace/protected-link; printf '#!/bin/sh\\nset -eu\\ntest \"$(cat source.txt)\" = owner-original\\nprintf test-script-ok\\n' > /workspace/test.sh; chmod 0755 /workspace/test.sh"
	output, err := runtime.runCheckoutHelper(ctx, machine, services, isolatedGitCommand(seed, "ku-seed-filesystem"))
	require.NoError(t, err, output)
	output, err = runtime.runRepositoryHelper(ctx, machine, services, isolatedGitCommand("set -eu; printf 'backend-only\\n' > /repositories/backend-only", "ku-seed-protected"), "", repositoryHelperInvocation{})
	require.NoError(t, err, output)
	check := func(t *testing.T, runID uuid.UUID) {
		t.Helper()
		script := "set -eu; test \"$(cat /workspace/source.txt)\" = owner-original; test \"$(cat /workspace/hardlink)\" = owner-original; test -x /workspace/test.sh; test ! -d /repositories/filesystem-runs/$1/verification; test ! -d /repositories/filesystem-runs/$1/verification-scratch; test \"$(cat /repositories/backend-only)\" = backend-only"
		output, err := runtime.runRepositoryHelper(ctx, machine, services, isolatedGitCommand(script, "ku-filesystem-preserved", runID.String()), "", repositoryHelperInvocation{})
		require.NoError(t, err, output)
	}
	t.Run("isolated-tooling-and-private-files", func(t *testing.T) {
		runID := uuid.New()
		script := "set -eu; test \"$(./test.sh)\" = test-script-ok; test \"$(cat .hidden)\" = hidden; test -z \"$GITHUB_TOKEN\"; test -z \"$ANTHROPIC_API_KEY\"; test -z \"$BASH_ENV\"; test \"$(ku-fixture-tool)\" = fixture-tool-ok; test \"$(stat -f -c %T /tmp)\" = xfs; test ! -e /repositories; test ! -e protected-link; printf 'test-mutated\\n' > source.txt; dd if=/dev/zero of=/tmp/compiler-cache bs=1M count=2 status=none; printf filesystem-tests-ok"
		execution, err := runtime.RunWorkspaceCommand(ctx, machine, services, runID, []string{"/bin/sh", "-c", script})
		require.NoError(t, err)
		require.Zero(t, execution.ExitCode)
		require.Equal(t, "filesystem-tests-ok", execution.Stdout)
		check(t, runID)
	})
	t.Run("nonzero-and-truncated-output", func(t *testing.T) {
		for _, status := range []int{0, 23} {
			runID := uuid.New()
			script := "set -eu; head -c " + strconv.Itoa(maxCapturedLogBytes+1) + " /dev/zero; head -c " + strconv.Itoa(maxCapturedLogBytes+1) + " /dev/zero >&2; exit " + strconv.Itoa(status)
			execution, err := runtime.RunWorkspaceCommand(ctx, machine, services, runID, []string{"/bin/sh", "-c", script})
			require.NoError(t, err)
			require.Equal(t, status, execution.ExitCode)
			require.True(t, execution.StdoutTruncated)
			require.True(t, execution.StderrTruncated)
			require.Len(t, execution.Stdout, maxCapturedLogBytes)
			require.Len(t, execution.Stderr, maxCapturedLogBytes)
			check(t, runID)
		}
	})
	t.Run("restart-reclaims-abandoned-copy", func(t *testing.T) {
		runID := uuid.New()
		for _, script := range []string{ensureWorkspaceVerificationDirectoryScript, claimWorkspaceVerificationScript, prepareWorkspaceVerificationScript} {
			output, err := runtime.runRepositoryHelper(ctx, machine, services, isolatedGitCommand(script, "ku-abandoned-copy", "/repositories", runID.String(), "/workspace", fixtureVerificationOwner), "", repositoryHelperInvocation{RunID: runID})
			require.NoError(t, err, output)
		}
		output, err := runtime.runRepositoryHelper(ctx, machine, services, isolatedGitCommand("set -eu; printf abandoned > /repositories/filesystem-runs/$1/verification/source.txt; printf abandoned > /repositories/filesystem-runs/$1/verification-scratch/old-cache", "ku-abandoned-test", runID.String()), "", repositoryHelperInvocation{RunID: runID})
		require.NoError(t, err, output)
		imageID, environment, err := runtime.verificationToolingImage(ctx, machine, services)
		require.NoError(t, err)
		options, err := workspaceVerificationOptions(machine, imageID, uuid.NewString(), runID, []string{"false"})
		require.NoError(t, err)
		options.Config.Env = environment
		options.Config.Labels[repositoryReservationVersionLabel] = "1"
		options.Config.Labels[repositoryReservationDeadlineLabel] = strconv.FormatInt(time.Now().Add(-time.Minute).UnixNano(), 10)
		old, err := runtime.client.ContainerCreate(ctx, options)
		require.NoError(t, err)
		restarted := &DockerRuntime{client: runtime.client}
		execution, err := restarted.RunWorkspaceCommand(ctx, machine, services, runID, []string{"/bin/sh", "-c", "set -eu; test \"$(cat source.txt)\" = owner-original; test ! -e /tmp/old-cache; printf restart-filesystem-ok"})
		require.NoError(t, err)
		require.Equal(t, "restart-filesystem-ok", execution.Stdout)
		_, err = runtime.client.ContainerInspect(ctx, old.ID, client.ContainerInspectOptions{})
		require.True(t, errdefs.IsNotFound(err))
		check(t, runID)
	})
	t.Run("cancellation-and-terminal-cleanup", func(t *testing.T) {
		runID := uuid.New()
		heldCtx, heldCancel := context.WithCancel(ctx)
		defer heldCancel()
		done := make(chan error, 1)
		go func() {
			_, err := runtime.RunWorkspaceCommand(heldCtx, machine, services, runID, []string{"/bin/sh", "-c", "set -eu; touch /tmp/started; sleep 120"})
			done <- err
		}()
		name := "ku-" + machine.RoutingKey + "-verification-" + runID.String()
		for {
			inspection, err := runtime.client.ContainerInspect(ctx, name, client.ContainerInspectOptions{})
			if err == nil && inspection.Container.State != nil && inspection.Container.State.Running {
				break
			}
			if err != nil && !errdefs.IsNotFound(err) {
				require.NoError(t, err)
			}
			select {
			case err := <-done:
				t.Fatalf("test exited before cancellation: %v", err)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case <-time.After(25 * time.Millisecond):
			}
		}
		run := &domain.DevMachineAgentRun{ID: runID, MachineID: machine.ID, WorkspaceID: machine.WorkspaceID, Status: domain.DevMachineAgentRunStatusCancelled}
		require.NoError(t, runtime.CancelAgent(ctx, machine, run))
		heldCancel()
		select {
		case err := <-done:
			require.Error(t, err)
			require.ErrorIs(t, err, context.Canceled)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		restarted := &DockerRuntime{client: runtime.client}
		_, err := restarted.CleanupAgentRun(ctx, machine, services, nil, run)
		require.NoError(t, err)
		check(t, runID)
		_, err = restarted.RunWorkspaceCommand(ctx, machine, services, runID, []string{"true"})
		require.ErrorIs(t, err, context.Canceled)
	})
}
