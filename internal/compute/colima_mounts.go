package compute

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type colimaMountEntry struct {
	Location string
	Writable bool
}

// ensureColimaHostMounts creates only the host directories that the Agent
// Computer already receives as Docker bind mounts. Colima needs the same
// paths mounted into its VM before Docker can expose them to the container.
func (d *Docker) ensureColimaHostMounts() ([]string, error) {
	// Chromium state lives in a Docker-managed volume. Binding the profile
	// through macOS virtiofs breaks Chromium's POSIX Singleton* lock symlinks;
	// only the user workspace needs a host/Colima mount.
	candidates := []string{d.Workspace}
	paths := make([]string, 0, len(candidates))
	seen := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		path, err := filepath.Abs(strings.TrimSpace(candidate))
		if err != nil {
			return nil, fmt.Errorf("resolve Colima mount path: %w", err)
		}
		path = filepath.Clean(path)
		if path == string(filepath.Separator) {
			return nil, errors.New("refusing to mount the host root into Colima")
		}
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		if err := os.MkdirAll(path, 0o700); err != nil {
			return nil, fmt.Errorf("create Colima mount directory %s: %w", path, err)
		}
		info, err := os.Lstat(path)
		if err != nil {
			return nil, fmt.Errorf("inspect Colima mount directory %s: %w", path, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("Colima mount path %s must not be a symbolic link", path)
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("Colima mount path %s is not a directory", path)
		}
		paths = append(paths, path)
	}
	return paths, nil
}

func colimaConfigPath(profile string) (string, error) {
	profile = strings.TrimSpace(profile)
	if profile == "" || profile == "." || profile == ".." || filepath.Base(profile) != profile {
		return "", errors.New("invalid Colima profile name")
	}
	root := strings.TrimSpace(os.Getenv("COLIMA_HOME"))
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home for Colima config: %w", err)
		}
		root = filepath.Join(home, ".colima")
	} else if root == "~" || strings.HasPrefix(root, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home for Colima config: %w", err)
		}
		root = filepath.Join(home, strings.TrimPrefix(root, "~/"))
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve Colima config root: %w", err)
	}
	return filepath.Join(root, profile, "colima.yaml"), nil
}

// ensureColimaMountConfig updates an existing profile config. A missing
// config is intentionally left to `colima start --mount ...`, which is the
// first-start path and lets Colima create its complete default config.
func ensureColimaMountConfig(profile string, required []string) (bool, bool, error) {
	configPath, err := colimaConfigPath(profile)
	if err != nil {
		return false, false, err
	}
	contents, err := os.ReadFile(configPath)
	if errors.Is(err, os.ErrNotExist) {
		return false, false, nil
	}
	if err != nil {
		return false, false, fmt.Errorf("read Colima config: %w", err)
	}
	updated, changed, err := addColimaMounts(string(contents), required)
	if err != nil {
		return true, false, fmt.Errorf("prepare Colima mounts: %w", err)
	}
	if !changed {
		return true, false, nil
	}
	info, err := os.Stat(configPath)
	if err != nil {
		return true, false, fmt.Errorf("inspect Colima config: %w", err)
	}
	if err := replaceColimaConfig(configPath, []byte(updated), info.Mode().Perm()); err != nil {
		return true, false, err
	}
	return true, true, nil
}

func addColimaMounts(contents string, required []string) (string, bool, error) {
	lines := strings.Split(contents, "\n")
	start, end, insertAt, inlineEmpty, entries, found := parseColimaMountBlock(lines)
	if !found {
		if len(required) == 0 {
			return contents, false, nil
		}
		if len(lines) > 0 && lines[len(lines)-1] != "" {
			lines = append(lines, "")
		}
		lines = append(lines, "mounts:")
		lines = append(lines, formatColimaMountLines(required)...)
		return strings.Join(lines, "\n"), true, nil
	}

	missing := make([]string, 0, len(required))
	for _, requiredPath := range required {
		requiredPath, err := filepath.Abs(requiredPath)
		if err != nil {
			return contents, false, fmt.Errorf("resolve required mount %q: %w", requiredPath, err)
		}
		requiredPath = filepath.Clean(requiredPath)
		covered := false
		for _, entry := range entries {
			if !mountCovers(entry.Location, requiredPath) {
				continue
			}
			if !entry.Writable {
				return contents, false, fmt.Errorf("required path %s is covered by a read-only Colima mount %s", requiredPath, entry.Location)
			}
			covered = true
			break
		}
		if !covered {
			missing = append(missing, requiredPath)
		}
	}
	if len(missing) == 0 {
		return contents, false, nil
	}

	addition := formatColimaMountLines(missing)
	if inlineEmpty {
		replacement := append([]string{"mounts:"}, addition...)
		lines = append(append([]string{}, lines[:start]...), append(replacement, lines[end:]...)...)
		return strings.Join(lines, "\n"), true, nil
	}
	if insertAt < 0 || insertAt > len(lines) {
		return contents, false, errors.New("could not locate the Colima mounts block")
	}
	lines = append(lines, make([]string, len(addition))...)
	copy(lines[insertAt+len(addition):], lines[insertAt:len(lines)-len(addition)])
	copy(lines[insertAt:insertAt+len(addition)], addition)
	return strings.Join(lines, "\n"), true, nil
}

func parseColimaMountBlock(lines []string) (start, end, insertAt int, inlineEmpty bool, entries []colimaMountEntry, found bool) {
	start, end, insertAt = -1, -1, -1
	for index, line := range lines {
		trimmed := strings.TrimSpace(line)
		switch trimmed {
		case "mounts:":
			start, found = index, true
		case "mounts: []":
			start, end, insertAt, inlineEmpty, found = index, index+1, index+1, true, true
		}
		if found {
			break
		}
	}
	if !found || inlineEmpty {
		return start, end, insertAt, inlineEmpty, nil, found
	}

	end = len(lines)
	insertAt = start + 1
	lastEntryLine := start
	for index := start + 1; index < len(lines); index++ {
		line := lines[index]
		trimmed := strings.TrimSpace(line)
		if trimmed != "" && !strings.HasPrefix(trimmed, "#") && len(line) > 0 && line[0] != ' ' && line[0] != '\t' {
			end = index
			break
		}
		if strings.HasPrefix(trimmed, "- location:") {
			location := strings.TrimSpace(strings.TrimPrefix(trimmed, "- location:"))
			entries = append(entries, colimaMountEntry{Location: normalizeColimaMountLocation(location)})
			lastEntryLine = index
			continue
		}
		if len(entries) > 0 && strings.HasPrefix(trimmed, "writable:") {
			entries[len(entries)-1].Writable = strings.EqualFold(strings.TrimSpace(strings.TrimPrefix(trimmed, "writable:")), "true")
			lastEntryLine = index
		}
	}
	insertAt = lastEntryLine + 1
	return start, end, insertAt, false, entries, true
}

func formatColimaMountLines(paths []string) []string {
	lines := make([]string, 0, len(paths)*2)
	for _, path := range paths {
		lines = append(lines, "  - location: "+filepath.Clean(path), "    writable: true")
	}
	return lines
}

func normalizeColimaMountLocation(value string) string {
	value = strings.Trim(strings.TrimSpace(value), "\"'")
	if value == "~" || strings.HasPrefix(value, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			value = filepath.Join(home, strings.TrimPrefix(value, "~/"))
		}
	}
	if absolute, err := filepath.Abs(value); err == nil {
		return filepath.Clean(absolute)
	}
	return filepath.Clean(value)
}

func mountCovers(mountPath, requiredPath string) bool {
	mountPath = normalizeColimaMountLocation(mountPath)
	requiredPath = normalizeColimaMountLocation(requiredPath)
	relative, err := filepath.Rel(mountPath, requiredPath)
	if err != nil {
		return false
	}
	return relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))
}

func replaceColimaConfig(path string, contents []byte, mode os.FileMode) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".colima-config-*")
	if err != nil {
		return fmt.Errorf("create temporary Colima config: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(mode); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("set temporary Colima config permissions: %w", err)
	}
	if _, err := temporary.Write(contents); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write temporary Colima config: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync temporary Colima config: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary Colima config: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace Colima config: %w", err)
	}
	return nil
}
