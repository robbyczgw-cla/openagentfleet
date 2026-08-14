package compute

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/robbyczgw-cla/openagentfleet/internal/preferences"
)

// ensureColimaResourceConfig updates only the top-level CPU, memory and disk
// values in an existing managed profile. Disk is grow-only: a request below
// the existing value is intentionally left untouched because Colima/Lima
// cannot safely shrink a VM disk.
func ensureColimaResourceConfig(profile string, resources ResourceConfig) (bool, bool, int, error) {
	configPath, err := colimaConfigPath(profile)
	if err != nil {
		return false, false, 0, err
	}
	contents, err := os.ReadFile(configPath)
	if errors.Is(err, os.ErrNotExist) {
		return false, false, 0, nil
	}
	if err != nil {
		return false, false, 0, fmt.Errorf("read Colima resource config: %w", err)
	}
	updated, changed, diskGiB, err := updateColimaResources(string(contents), resources)
	if err != nil {
		return true, false, 0, err
	}
	if !changed {
		return true, false, diskGiB, nil
	}
	info, err := os.Stat(configPath)
	if err != nil {
		return true, false, diskGiB, fmt.Errorf("inspect Colima resource config: %w", err)
	}
	if err := replaceColimaConfig(configPath, []byte(updated), info.Mode().Perm()); err != nil {
		return true, false, diskGiB, err
	}
	return true, true, diskGiB, nil
}

func updateColimaResources(contents string, resources ResourceConfig) (string, bool, int, error) {
	resources = resources.Normalize()
	lines := strings.Split(contents, "\n")
	values := map[string]string{
		"cpu":    strconv.Itoa(resources.CPUs),
		"memory": strconv.Itoa(resources.MemoryGiB),
		"disk":   strconv.Itoa(resources.DiskGiB),
	}
	found := make(map[string]bool, len(values))
	currentDisk := 0
	changed := false
	for index, line := range lines {
		if len(line) == 0 || line[0] == ' ' || line[0] == '\t' || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		for key, desired := range values {
			prefix := key + ":"
			if !strings.HasPrefix(line, prefix) {
				continue
			}
			currentText := strings.TrimSpace(strings.TrimSpace(strings.TrimPrefix(line, prefix)))
			currentText = strings.TrimSpace(strings.SplitN(currentText, "#", 2)[0])
			if currentText == "" {
				return contents, false, 0, fmt.Errorf("Colima resource %s is empty", key)
			}
			if key == "disk" {
				parsed, err := strconv.Atoi(currentText)
				if err != nil || parsed < 1 {
					return contents, false, 0, fmt.Errorf("Colima disk value %q is invalid", currentText)
				}
				currentDisk = parsed
				if parsed >= resources.DiskGiB {
					found[key] = true
					break
				}
			}
			if key == "memory" {
				if parsed, err := strconv.ParseFloat(currentText, 32); err != nil || parsed <= 0 {
					return contents, false, 0, fmt.Errorf("Colima memory value %q is invalid", currentText)
				}
			}
			if currentText != desired {
				lines[index] = key + ": " + desired
				changed = true
			}
			found[key] = true
			break
		}
	}
	for _, key := range []string{"cpu", "memory", "disk"} {
		if found[key] {
			continue
		}
		lines = append(lines, key+": "+values[key])
		changed = true
		if key == "disk" {
			currentDisk = resources.DiskGiB
		}
	}
	return strings.Join(lines, "\n"), changed, currentDisk, nil
}

// configureColimaSwap manages a small explicit swap file in the Linux guest.
// Colima 0.10.x has no --swap flag, and macOS host swap is not visible inside
// the guest. The operation is idempotent and never touches the user's host
// filesystem; swapGiB=0 removes only this app-owned guest file.
func configureColimaSwap(parent context.Context, binary, profile string, swapGiB int) error {
	if swapGiB < preferences.MinComputerSwapGiB || swapGiB > preferences.MaxComputerSwapGiB {
		return swapUnsupportedError(profile)
	}
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	const swapPath = "/var/lib/openagentfleet-agent-computer.swap"
	wantBytes := int64(swapGiB) * 1024 * 1024 * 1024
	script := fmt.Sprintf(`set -eu
file=%q
want=%d
current=0
if [ -f "$file" ]; then current=$(stat -c %%s "$file" 2>/dev/null || echo 0); fi
active=0
if swapon --show=NAME --noheadings 2>/dev/null | grep -Fx "$file" >/dev/null 2>&1; then active=1; fi
if [ "$want" -eq 0 ]; then
  if [ "$active" -eq 1 ]; then swapoff "$file"; fi
  rm -f "$file"
  exit 0
fi
if [ "$current" -eq "$want" ] && [ "$active" -eq 1 ]; then exit 0; fi
if [ "$active" -eq 1 ]; then swapoff "$file"; fi
fallocate -l "$want" "$file"
chmod 600 "$file"
mkswap "$file" >/dev/null
swapon "$file"
`, swapPath, wantBytes)
	command := newCommandContext(ctx, binary, "ssh", "--profile", profile, "--", "sudo", "sh", "-c", script)
	if output, err := command.CombinedOutput(); err != nil {
		detail := compact(string(output))
		if detail != "" {
			return fmt.Errorf("configure %d GiB Colima guest swap: %w: %s", swapGiB, err, detail)
		}
		return fmt.Errorf("configure %d GiB Colima guest swap: %w", swapGiB, err)
	}
	return nil
}
