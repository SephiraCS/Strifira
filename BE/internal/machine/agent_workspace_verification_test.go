package machine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/SephiraCS/strifira-backend/internal/agent"
	"github.com/SephiraCS/strifira-backend/internal/domain"
	"github.com/google/uuid"
	"github.com/moby/moby/api/types/container"
	"github.com/stretchr/testify/require"
)

func TestAgentWorkspaceRequiredTestsWithoutRepository(t *testing.T) {
	for _, test := range []struct {
		name      string
		execution *CommandExecution
		err       error
		status    domain.DevMachineAgentRunStatus
	}{
		{"passes", &CommandExecution{Stdout: "workspace-tests-ok\n"}, nil, domain.DevMachineAgentRunStatusSucceeded},
		{"fails", &CommandExecution{ExitCode: 23}, nil, domain.DevMachineAgentRunStatusFailed},
		{"execution error", nil, errors.New("snapshot unavailable"), domain.DevMachineAgentRunStatusFailed},
		{"timeout", nil, context.DeadlineExceeded, domain.DevMachineAgentRunStatusTimeout},
	} {
		t.Run(test.name, func(t *testing.T) {
			manager, store, runtime, run := verificationFixture(t)
			store.machine.RepoOwner, store.machine.RepoName, store.machine.RepoURL = "", "", ""
			command, err := json.Marshal([]string{"sh", "test.sh"})
			require.NoError(t, err)
			run.TestCommand = new(json.RawMessage(command))
			runtime.workspaceExecution, runtime.workspaceCommandErr = test.execution, test.err
			if test.name == "timeout" {
				run.MaxRuntimeSeconds = 1
				runtime.onCandidateCommandContext = func(ctx context.Context) {
					<-ctx.Done()
					runtime.workspaceCommandErr = ctx.Err()
				}
			}
			require.NoError(t, manager.runAgent(t.Context(), store.machine, &domain.DevMachineOperation{AgentRunID: &run.ID, Generation: 1}))
			require.Equal(t, test.status, store.completedRun.Status)
			require.Equal(t, []string{"agent", "workspace-test"}, runtime.repositoryCalls)
			require.Equal(t, run.ID, runtime.testedRunID)
			require.Nil(t, runtime.agentCheckout)
			require.Zero(t, runtime.publishedBranches)
			if test.status == domain.DevMachineAgentRunStatusSucceeded {
				require.Equal(t, "passed", store.completedRun.TestStatus)
			} else {
				require.Equal(t, "failed", store.completedRun.TestStatus)
			}
		})
	}
}

func TestAgentWorkspaceCannotPublishWithoutRepository(t *testing.T) {
	manager, store, runtime, run := verificationFixture(t)
	run.PushBranch = true
	result := &agent.RunResult{}
	require.ErrorContains(t, manager.verifyAgentRun(t.Context(), store.machine, run, nil, nil, result, nil), "no selected repository")
	require.Empty(t, runtime.workspaceCommands)
}

func TestAgentTestsMarkTruncatedStreamsAndRedactIncompleteSecrets(t *testing.T) {
	for _, exit := range []int{0, 23} {
		t.Run(fmt.Sprint(exit), func(t *testing.T) {
			manager, store, _, run := verificationFixture(t)
			result := &agent.RunResult{}
			err := manager.verifyAgentTests(t.Context(), store.machine, run, result, map[string]string{"TOKEN": "secret-token"}, []string{"true"}, "", func() (*CommandExecution, error) {
				return &CommandExecution{Stdout: "first secret-token\nsecret-t", Stderr: "last secret-", StdoutTruncated: true, StderrTruncated: true, ExitCode: exit}, nil
			})
			if exit == 0 {
				require.NoError(t, err)
				require.Equal(t, "passed", result.TestStatus)
			} else {
				require.ErrorContains(t, err, "required test command failed")
				require.Equal(t, "failed", result.TestStatus)
			}
			require.Len(t, store.logChunks, 1)
			require.True(t, store.logChunks[0].Truncated)
			require.Contains(t, store.logChunks[0].Content, "stdout truncated")
			require.Contains(t, store.logChunks[0].Content, "stderr truncated")
			require.NotContains(t, store.logChunks[0].Content, "secret")
			require.NotEmpty(t, result.RiskNotes)
			var event map[string]any
			require.NoError(t, json.Unmarshal(store.events[len(store.events)-1].Payload, &event))
			require.Equal(t, true, event["output_truncated"])
			require.Equal(t, exit == 0, event["verified"])
		})
	}
}

type workspaceVerificationFixture struct {
	store, source, run string
}

func newWorkspaceVerificationFixture(t *testing.T) workspaceVerificationFixture {
	t.Helper()
	root := t.TempDir()
	f := workspaceVerificationFixture{store: filepath.Join(root, "protected"), source: filepath.Join(root, "workspace"), run: uuid.NewString()}
	require.NoError(t, os.Mkdir(f.store, 0o700))
	require.NoError(t, os.Mkdir(f.source, 0o755))
	return f
}

func (f workspaceVerificationFixture) script(t *testing.T, script, owner string) (string, error) {
	t.Helper()
	command := exec.CommandContext(t.Context(), "/bin/sh", "-c", script, "strifira-workspace-test", f.store, f.run, f.source, owner)
	command.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir()}
	output, err := command.CombinedOutput()
	return string(output), err
}

func (f workspaceVerificationFixture) prepare(t *testing.T, owner string) string {
	t.Helper()
	for _, script := range []string{ensureWorkspaceVerificationDirectoryScript, claimWorkspaceVerificationScript, prepareWorkspaceVerificationScript} {
		output, err := f.script(t, script, owner)
		require.NoError(t, err, output)
	}
	return filepath.Join(f.store, "filesystem-runs", f.run, "verification")
}

func TestWorkspaceVerificationCopyHasPrivateInodesAndPreservesLiveFiles(t *testing.T) {
	f := newWorkspaceVerificationFixture(t)
	name := "space\nand newline.sh"
	file := filepath.Join(f.source, name)
	require.NoError(t, os.WriteFile(file, []byte("original\n"), 0o700))
	require.NoError(t, os.Link(file, filepath.Join(f.source, ".hidden-hardlink")))
	require.NoError(t, os.Symlink(name, filepath.Join(f.source, "relative-symlink")))
	outside := filepath.Join(t.TempDir(), "outside")
	require.NoError(t, os.WriteFile(outside, []byte("outside\n"), 0o600))
	require.NoError(t, os.Symlink(outside, filepath.Join(f.source, "absolute-symlink")))
	copy := f.prepare(t, fixtureVerificationOwner)
	before, err := os.Stat(file)
	require.NoError(t, err)
	copied, err := os.Stat(filepath.Join(copy, name))
	require.NoError(t, err)
	require.False(t, os.SameFile(before, copied))
	require.Equal(t, before.Mode().Perm(), copied.Mode().Perm())
	link, err := os.Readlink(filepath.Join(copy, "absolute-symlink"))
	require.NoError(t, err)
	require.Equal(t, outside, link)
	require.NoError(t, os.WriteFile(file, []byte("later live edit\n"), 0o700))
	contents, err := os.ReadFile(filepath.Join(copy, name))
	require.NoError(t, err)
	require.Equal(t, "original\n", string(contents))
	require.NoError(t, os.WriteFile(filepath.Join(copy, name), []byte("test edit\n"), 0o700))
	contents, err = os.ReadFile(file)
	require.NoError(t, err)
	require.Equal(t, "later live edit\n", string(contents))
	output, err := f.script(t, cleanupWorkspaceVerificationScript, fixtureVerificationOwner)
	require.NoError(t, err, output)
	require.NoDirExists(t, copy)
	require.FileExists(t, file)
	require.FileExists(t, outside)
}

func TestWorkspaceVerificationRecoveryRejectsStaleAndTerminalOwners(t *testing.T) {
	f := newWorkspaceVerificationFixture(t)
	file := filepath.Join(f.source, "source.txt")
	require.NoError(t, os.WriteFile(file, []byte("before\n"), 0o600))
	copy := f.prepare(t, fixtureVerificationOwner)
	require.NoError(t, os.WriteFile(filepath.Join(copy, "source.txt"), []byte("abandoned test edit\n"), 0o600))
	require.NoError(t, os.WriteFile(file, []byte("latest\n"), 0o600))
	newOwner := "1000000000000000001:22222222-2222-2222-2222-222222222222"
	f.prepare(t, newOwner)
	for _, script := range []string{claimWorkspaceVerificationScript, prepareWorkspaceVerificationScript, cleanupWorkspaceVerificationScript} {
		output, err := f.script(t, script, fixtureVerificationOwner)
		require.Error(t, err, output)
	}
	contents, err := os.ReadFile(filepath.Join(copy, "source.txt"))
	require.NoError(t, err)
	require.Equal(t, "latest\n", string(contents))
	output, err := f.script(t, cleanupTerminalWorkspaceVerificationScript, "")
	require.NoError(t, err, output)
	require.NoDirExists(t, copy)
	output, err = f.script(t, cleanupTerminalWorkspaceVerificationScript, "")
	require.NoError(t, err, output)
	for _, script := range []string{ensureWorkspaceVerificationDirectoryScript, claimWorkspaceVerificationScript, prepareWorkspaceVerificationScript} {
		output, err := f.script(t, script, newOwner)
		require.Error(t, err, output)
	}
	require.NoDirExists(t, copy)
	require.FileExists(t, file)
}

func TestWorkspaceVerificationRejectsSpecialFilesAndSymlinkedPrivateDirectories(t *testing.T) {
	for _, mode := range []string{"fifo", "private directory symlink"} {
		t.Run(mode, func(t *testing.T) {
			f := newWorkspaceVerificationFixture(t)
			output, err := f.script(t, ensureWorkspaceVerificationDirectoryScript, "")
			require.NoError(t, err, output)
			output, err = f.script(t, claimWorkspaceVerificationScript, fixtureVerificationOwner)
			require.NoError(t, err, output)
			if mode == "fifo" {
				output, err := exec.CommandContext(t.Context(), "mkfifo", filepath.Join(f.source, "pipe")).CombinedOutput()
				require.NoError(t, err, string(output))
			} else {
				private := filepath.Join(f.store, "filesystem-runs", f.run, "verification")
				require.NoError(t, os.Remove(private))
				require.NoError(t, os.Symlink(f.source, private))
			}
			output, err = f.script(t, prepareWorkspaceVerificationScript, fixtureVerificationOwner)
			require.Error(t, err, output)
		})
	}
}

func TestWorkspaceVerificationMountPolicy(t *testing.T) {
	machine := fixtureHelperMachine()
	options, err := workspaceVerificationOptions(machine, fixtureHelperImageID, uuid.NewString(), uuid.New(), []string{"true"})
	require.NoError(t, err)
	baseline := container.InspectResponse{Config: options.Config, HostConfig: options.HostConfig}
	require.True(t, protectedWorkspaceVerificationMounts(baseline, machine))
	encoded, err := json.Marshal(baseline)
	require.NoError(t, err)
	for name, mutate := range map[string]func(*container.InspectResponse){
		"foreign machine":   func(i *container.InspectResponse) { i.Config.Labels["com.strifira.machine-id"] = uuid.NewString() },
		"foreign workspace": func(i *container.InspectResponse) { i.Config.Labels["com.strifira.workspace-id"] = uuid.NewString() },
		"foreign run":       func(i *container.InspectResponse) { i.Config.Labels["com.strifira.agent-run-id"] = uuid.NewString() },
		"live workspace":    func(i *container.InspectResponse) { i.HostConfig.Mounts[0].VolumeOptions.Subpath = "workspace" },
		"protected store":   func(i *container.InspectResponse) { i.HostConfig.Mounts[0].VolumeOptions.Subpath = "repositories" },
		"traversal":         func(i *container.InspectResponse) { i.HostConfig.Mounts[0].VolumeOptions.Subpath += "/../.." },
		"foreign scratch": func(i *container.InspectResponse) {
			i.HostConfig.Mounts[1].VolumeOptions.Subpath = "repositories/filesystem-runs/" + uuid.NewString() + "/verification-scratch"
		},
		"mutable image":    func(i *container.InspectResponse) { i.Config.Image = "latest" },
		"privileged":       func(i *container.InspectResponse) { i.HostConfig.Privileged = true },
		"root":             func(i *container.InspectResponse) { i.Config.User = "0:0" },
		"writable root":    func(i *container.InspectResponse) { i.HostConfig.ReadonlyRootfs = false },
		"missing cap drop": func(i *container.InspectResponse) { i.HostConfig.CapDrop = nil },
		"bind":             func(i *container.InspectResponse) { i.HostConfig.Binds = []string{"/workspace:/leak"} },
		"network":          func(i *container.InspectResponse) { i.HostConfig.NetworkMode = "host" },
		"extra mount": func(i *container.InspectResponse) {
			i.HostConfig.Mounts = append(i.HostConfig.Mounts, i.HostConfig.Mounts[0])
		},
	} {
		t.Run(name, func(t *testing.T) {
			var inspection container.InspectResponse
			require.NoError(t, json.Unmarshal(encoded, &inspection))
			mutate(&inspection)
			require.False(t, protectedWorkspaceVerificationMounts(inspection, machine))
		})
	}
	_, err = workspaceVerificationOptions(machine, fixtureHelperImageID, uuid.NewString(), uuid.New(), []string{"sh", "bad\x00argument"})
	require.Error(t, err)
	_, err = (&DockerRuntime{}).RunWorkspaceCommand(t.Context(), machine, nil, uuid.New(), []string{"true"})
	require.ErrorContains(t, err, "deadline")
}
