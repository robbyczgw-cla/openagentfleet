package isolation

import (
	"fmt"
	"strconv"
	"strings"
)

const workerLabel = "io.openagentfleet.worker-isolation"

func dockerArgs(spec Spec) []string {
	args := []string{
		"run",
		"--rm",
		"--pull=never",
		"--name", containerName(spec.SessionID),
		"--label", workerLabel + "=true",
		"--label", "io.openagentfleet.session=" + spec.SessionID,
		"--label", "io.openagentfleet.isolation-version=1",
		"--user", spec.User,
		"--read-only",
		"--cap-drop", "ALL",
		"--security-opt", "no-new-privileges=true",
		"--pids-limit", strconv.FormatUint(uint64(spec.Limits.PIDs), 10),
		"--cpus", formatCPUs(spec.Limits.CPUMilli),
		"--memory", strconv.FormatUint(spec.Limits.MemoryBytes, 10),
		"--network", "none",
	}
	for _, item := range spec.Tmpfs {
		args = append(args, "--tmpfs", fmt.Sprintf("%s:rw,nosuid,nodev,noexec,size=%d,mode=%o", item.GuestPath, item.SizeBytes, item.Mode))
	}
	for _, mount := range spec.Mounts {
		value := "type=bind,source=" + mount.HostPath + ",target=" + mount.GuestPath
		if mount.Mode == MountReadOnly {
			value += ",readonly"
		}
		args = append(args, "--mount", value)
	}
	for _, secret := range spec.Secrets {
		// This is a controller-generated opaque locator, not a credential.
		args = append(args, "--env", secret.Environment+"="+string(secret.Source)+":"+secret.Reference)
	}
	args = append(args, "--workdir", spec.Workdir, spec.Image)
	args = append(args, spec.Command...)
	return args
}

func cleanupPlan(sessionID string) CleanupPlan {
	name := containerName(sessionID)
	return CleanupPlan{
		ContainerName: name,
		OwnerLabels:   []string{workerLabel + "=true", "io.openagentfleet.session=" + sessionID},
		StopArgs:      []string{"stop", "--time", "10", name},
		RemoveArgs:    []string{"rm", "--force", name},
		OrphanQuery:   []string{"ps", "--all", "--filter", "label=" + workerLabel + "=true", "--format", "{{.ID}}"},
	}
}

func containerName(sessionID string) string {
	return "openagentfleet-worker-" + sessionID
}

func formatCPUs(milli uint32) string {
	whole := milli / 1000
	fraction := milli % 1000
	if fraction == 0 {
		return strconv.FormatUint(uint64(whole), 10)
	}
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%d.%03d", whole, fraction), "0"), ".")
}
