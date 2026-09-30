package machine

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/containerd/errdefs"
	"github.com/google/uuid"
	"github.com/ku/ku-backend/internal/agent"
	"github.com/ku/ku-backend/internal/domain"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
	"github.com/stretchr/testify/require"
)

// This opt-in qualification uses only an owned volume, network and containers.
// Its externally routed test bridge permits a private development provider;
// machine egress-policy qualification remains a separate lifecycle test.
func TestDockerRuntimeAgentQualification(t *testing.T) {
	image := os.Getenv("KU_DOCKER_AGENT_IMAGE")
	if image == "" {
		t.Skip("set KU_DOCKER_AGENT_IMAGE to an already built Claude agent image")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	runtime, err := NewDockerRuntime(DockerConfig{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = runtime.client.Close() })
	require.NoError(t, runtime.Ping(ctx))

	routingKey := "agent-test-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	volumeName, networkName := "ku-workspace-"+routingKey, "ku-machine-"+routingKey
	machine := &domain.DevMachine{
		ID: uuid.New(), WorkspaceID: uuid.New(), RoutingKey: routingKey,
		CPUMillis: 2000, MemoryMB: 3072, PidsLimit: 512, DiskGB: 1,
		BaseBranch: "main", WorkingBranch: "ku/docker-smoke",
		DockerNetworkName: &networkName, WorkspaceVolumeName: &volumeName,
	}
	labels := machineLabels(machine)
	created, err := runtime.ensureNetwork(ctx, networkName, false, labels)
	require.NoError(t, err)
	require.True(t, created)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		containers, listErr := runtime.client.ContainerList(cleanupCtx, client.ContainerListOptions{All: true})
		if listErr == nil {
			for _, item := range containers.Items {
				if item.Labels["com.ku.machine-id"] == machine.ID.String() {
					_, removeErr := runtime.client.ContainerRemove(cleanupCtx, item.ID, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true})
					require.NoError(t, removeErr)
				}
			}
		}
		require.NoError(t, listErr)
		require.NoError(t, runtime.removeManagedVolume(cleanupCtx, volumeName, machine.ID))
		_, removeErr := runtime.client.NetworkRemove(cleanupCtx, networkName, client.NetworkRemoveOptions{})
		require.NoError(t, removeErr)
		t.Log("owned Docker containers, quota volume and network removed")
	})
	created, err = runtime.ensureVolume(ctx, volumeName, labels, machine.DiskGB)
	require.NoError(t, err)
	require.True(t, created)
	require.NoError(t, runtime.prepareWorkspaceVolume(ctx, machine, volumeName, image))

	developer, err := runtime.client.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config: &container.Config{
			Image: image, User: "1000:1000", WorkingDir: "/workspace",
			Entrypoint: []string{"/bin/sleep"}, Cmd: []string{"180"},
			Env: []string{"HOME=/home/ku"}, Labels: labels,
		},
		HostConfig: runtime.secureHostConfig(machine, "agent", networkName, volumeName),
		Name:       "ku-" + routingKey + "-developer",
	})
	require.NoError(t, err)
	_, err = runtime.client.ContainerStart(ctx, developer.ID, client.ContainerStartOptions{})
	require.NoError(t, err)
	services := []domain.DevMachineService{{ServiceType: "ide", ContainerID: &developer.ID}}
	_, err = runtime.execOutput(ctx, developer.ID, []string{"/bin/sh", "-c", `set -eu
git init --quiet -b main
git config user.name KuSmoke
git config user.email ku-smoke@localhost
printf '%s\n' '#!/bin/sh' 'printf "%s\n" old-value' > greeting.sh
printf '%s\n' '#!/bin/sh' 'set -eu' '[ "$(sh greeting.sh)" = ku-docker-agent-ok ]' > test.sh
git add greeting.sh test.sh
git commit --quiet -m 'Initialize disposable Docker fixture'
git update-ref refs/remotes/origin/main HEAD
git switch --quiet -c ku/docker-smoke
`})
	require.NoError(t, err)
	toolchain, err := runtime.RunWorkspaceCommand(ctx, services, nil, []string{"sh", "-c", `set -eu
printf '%s\n' 'package main' 'import "fmt"' 'func main() { fmt.Println("ku-docker-toolchain-ok") }' > /tmp/ku-toolchain.go
go build -o /tmp/ku-toolchain /tmp/ku-toolchain.go
[ "$(/tmp/ku-toolchain)" = ku-docker-toolchain-ok ]
`})
	require.NoError(t, err)
	require.Zero(t, toolchain.ExitCode)
	require.Empty(t, toolchain.Stderr)
	t.Log("container Go compiler and temporary binary execution passed")

	if os.Getenv("KU_DOCKER_AGENT_LIVE") == "1" {
		key := os.Getenv("KU_DOCKER_AGENT_API_KEY")
		require.NotEmpty(t, key, "provide a dedicated key in the test process environment")
		provider := agent.NewClaudeCodeProvider(image)
		extraArgs := []string{"--setting-sources", "", "--strict-mcp-config", "--no-session-persistence", "--max-budget-usd", "0.50"}
		if model := os.Getenv("KU_DOCKER_AGENT_MODEL"); model != "" {
			extraArgs = append(extraArgs, "--model", model)
		}
		invocation, invocationErr := provider.BuildInvocation(agent.RunInput{
			Mode:      agent.ModeAutonomous,
			Prompt:    "Edit only greeting.sh to print exactly ku-docker-agent-ok on one line, then run sh test.sh. Do not inspect credentials or other directories, use the network, commit or push. Finish with a brief summary.",
			ExtraArgs: extraArgs,
		})
		require.NoError(t, invocationErr)
		secrets := map[string]string{"ANTHROPIC_API_KEY": key, "DISABLE_AUTOUPDATER": "1"}
		if baseURL := os.Getenv("KU_DOCKER_AGENT_BASE_URL"); baseURL != "" {
			secrets["ANTHROPIC_BASE_URL"] = baseURL
			secrets["HTTP_PROXY"], secrets["HTTPS_PROXY"] = "", ""
		}
		argv, marshalErr := json.Marshal(invocation.Argv)
		require.NoError(t, marshalErr)
		run := &domain.DevMachineAgentRun{ID: uuid.New(), Mode: "autonomous", CommandArgv: argv}
		var streamed atomic.Bool
		liveCtx := context.WithValue(ctx, agentLogObserverKey{}, agentLogObserver(func(id, stream, content string) {
			inspection, inspectErr := runtime.client.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
			if inspectErr == nil && inspection.Container.State.Running && stream == "stdout" && len(provider.ParseEvents([]byte(content))) > 0 {
				streamed.Store(true)
			}
		}))
		execution, runErr := runtime.RunAgent(liveCtx, machine, run, &domain.DevMachineAgentProvider{ImageRef: image}, nil, secrets)
		require.NoError(t, runErr)
		require.Zero(t, execution.ExitCode)
		result := provider.ParseResult(execution.Stdout, execution.Stderr, execution.ExitCode)
		require.Equal(t, "succeeded", result.Status)
		require.NotEmpty(t, result.Summary)
		require.True(t, streamed.Load(), "provider events must arrive before the container exits")
		inspection, inspectErr := runtime.client.ContainerInspect(ctx, execution.ContainerID, client.ContainerInspectOptions{})
		require.NoError(t, inspectErr)
		require.Equal(t, "1000:1000", inspection.Container.Config.User)
		require.True(t, inspection.Container.HostConfig.ReadonlyRootfs)
		require.False(t, inspection.Container.HostConfig.Privileged)
		require.Contains(t, inspection.Container.HostConfig.CapDrop, "ALL")
		require.Contains(t, inspection.Container.HostConfig.SecurityOpt, "no-new-privileges=true")
		require.EqualValues(t, 1_127_428_915, inspection.Container.HostConfig.Memory)
		require.EqualValues(t, 700_000_000, inspection.Container.HostConfig.NanoCPUs)
		require.EqualValues(t, 179, *inspection.Container.HostConfig.PidsLimit)
		for _, value := range inspection.Container.Config.Env {
			require.False(t, strings.Contains(value, key), "key must not be recorded in container configuration")
		}
		t.Log("live coding, active JSONL streaming, terminal normalization and container resource/security limits passed")
	} else {
		_, err = runtime.execOutput(ctx, developer.ID, []string{"/bin/sh", "-c", `printf '%s\n' '#!/bin/sh' 'printf "%s\n" ku-docker-agent-ok' > greeting.sh`})
		require.NoError(t, err)
	}

	independent, err := runtime.RunWorkspaceCommand(ctx, services, nil, []string{"sh", "test.sh"})
	require.NoError(t, err)
	require.Zero(t, independent.ExitCode)
	require.Empty(t, independent.Stderr)
	failure, err := runtime.RunWorkspaceCommand(ctx, services, nil, []string{"sh", "-c", "exit 23"})
	require.NoError(t, err)
	require.Equal(t, 23, failure.ExitCode)
	gitState, err := runtime.GitState(ctx, machine, services, nil)
	require.NoError(t, err)
	require.Equal(t, "ku/docker-smoke", gitState.Branch)
	require.Equal(t, []string{"greeting.sh"}, gitState.ChangedFiles)
	remotes, err := runtime.execOutput(ctx, developer.ID, []string{"git", "remote"})
	require.NoError(t, err)
	require.Empty(t, remotes)
	t.Log("independent tests passed; exit 23 retained; actual Git edit and no remotes verified")

	commandCtx, commandCancel := context.WithCancel(ctx)
	defer commandCancel()
	commandDone := make(chan error, 1)
	go func() {
		_, commandErr := runtime.RunWorkspaceCommand(commandCtx, services, nil, []string{"sh", "-c", "touch /tmp/command-started; (sleep 3; touch /tmp/command-survived) & wait"})
		commandDone <- commandErr
	}()
	require.Eventually(t, func() bool {
		_, checkErr := runtime.execOutput(ctx, developer.ID, []string{"test", "-f", "/tmp/command-started"})
		return checkErr == nil
	}, 5*time.Second, 50*time.Millisecond)
	commandCancel()
	select {
	case <-commandDone:
	case <-time.After(15 * time.Second):
		t.Fatal("independent command cancellation did not finish")
	}
	time.Sleep(3200 * time.Millisecond)
	_, err = runtime.execOutput(ctx, developer.ID, []string{"test", "!", "-f", "/tmp/command-survived"})
	require.NoError(t, err, "cancelled command descendants must not survive")

	argv, err := json.Marshal([]string{"sh", "-c", "printf '%s\\n' cancellation-ready; sleep 120"})
	require.NoError(t, err)
	run := &domain.DevMachineAgentRun{ID: uuid.New(), Mode: "autonomous", CommandArgv: argv}
	started := make(chan struct{}, 1)
	runCtx := context.WithValue(ctx, agentLogObserverKey{}, agentLogObserver(func(_, _, content string) {
		if strings.Contains(content, "cancellation-ready") {
			select {
			case started <- struct{}{}:
			default:
			}
		}
	}))
	agentDone := make(chan error, 1)
	go func() {
		_, runErr := runtime.RunAgent(runCtx, machine, run, &domain.DevMachineAgentProvider{ImageRef: image}, nil, nil)
		agentDone <- runErr
	}()
	select {
	case <-started:
	case runErr := <-agentDone:
		t.Fatalf("cancellation fixture stopped before readiness: %v", runErr)
	case <-time.After(10 * time.Second):
		t.Fatal("cancellation fixture did not start")
	}
	require.NoError(t, runtime.CancelAgent(ctx, run))
	select {
	case <-agentDone:
	case <-time.After(15 * time.Second):
		t.Fatal("cancelled agent wait did not finish")
	}
	_, err = runtime.client.ContainerInspect(ctx, "ku-"+routingKey+"-agent-"+run.ID.String(), client.ContainerInspectOptions{})
	require.True(t, errdefs.IsNotFound(err))
	_, err = runtime.execOutput(ctx, developer.ID, []string{"true"})
	require.NoError(t, err, "developer container must remain usable after cancellation")
	t.Log("agent and independent command cancellation passed; developer container preserved")
}
