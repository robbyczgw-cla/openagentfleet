package skills

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/robbyczgw-cla/openagentfleet/internal/domain"
)

const maxSkillBytes = 64 * 1024

type root struct {
	path   string
	source string
}

func Discover(workspace string) ([]domain.Skill, error) {
	roots := []root{
		{path: filepath.Join(workspace, ".agents", "skills"), source: "workspace/.agents"},
		{path: filepath.Join(workspace, ".claude", "skills"), source: "workspace/.claude"},
		{path: filepath.Join(workspace, ".codex", "skills"), source: "workspace/.codex"},
		{path: filepath.Join(workspace, "skills"), source: "workspace"},
	}
	if home, err := os.UserHomeDir(); err == nil {
		roots = append(roots,
			root{path: filepath.Join(home, ".codex", "skills"), source: "managed/codex"},
			root{path: filepath.Join(home, ".claude", "skills"), source: "managed/claude"},
			root{path: filepath.Join(home, ".config", "hermes", "skills"), source: "managed/hermes"},
			root{path: filepath.Join(home, ".openclaw", "skills"), source: "managed/openclaw"},
		)
	}

	result := make([]domain.Skill, 0)
	seen := make(map[string]struct{})
	var firstErr error
	for _, candidate := range roots {
		resolved, err := filepath.Abs(candidate.path)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		entries, err := os.ReadDir(resolved)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			skillPath := filepath.Join(resolved, entry.Name(), "SKILL.md")
			if _, ok := seen[skillPath]; ok {
				continue
			}
			item, parseErr := parse(skillPath, candidate.source, entry.Name())
			if errors.Is(parseErr, os.ErrNotExist) {
				continue
			}
			if parseErr != nil {
				if firstErr == nil {
					firstErr = parseErr
				}
				continue
			}
			seen[skillPath] = struct{}{}
			result = append(result, item)
		}
	}
	if firstErr != nil && len(result) == 0 {
		return result, firstErr
	}
	return result, nil
}

func parse(path, source, fallbackName string) (domain.Skill, error) {
	file, err := os.Open(path)
	if err != nil {
		return domain.Skill{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return domain.Skill{}, err
	}
	if info.Size() > maxSkillBytes {
		return domain.Skill{}, fmt.Errorf("skill %s exceeds %d bytes", fallbackName, maxSkillBytes)
	}

	item := domain.Skill{
		ID:        source + ":" + fallbackName,
		Name:      fallbackName,
		Path:      path,
		Source:    source,
		Eligible:  true,
		UpdatedAt: info.ModTime().UTC().Format(time.RFC3339),
	}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), maxSkillBytes)
	inFrontmatter := false
	frontmatterSeen := false
	paragraphStarted := false
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "---" && !frontmatterSeen {
			frontmatterSeen = true
			inFrontmatter = true
			continue
		}
		if line == "---" && inFrontmatter {
			inFrontmatter = false
			continue
		}
		if inFrontmatter {
			key, value, ok := strings.Cut(line, ":")
			if !ok {
				continue
			}
			switch strings.TrimSpace(key) {
			case "name":
				if strings.TrimSpace(value) != "" {
					item.Name = strings.Trim(strings.TrimSpace(value), "\"'")
				}
			case "description":
				item.Description = strings.Trim(strings.TrimSpace(value), "\"'")
			case "disabled":
				if strings.EqualFold(strings.TrimSpace(value), "true") {
					item.Eligible = false
					item.Detail = "disabled by SKILL.md metadata"
				}
			case "enabled":
				if strings.EqualFold(strings.TrimSpace(value), "false") {
					item.Eligible = false
					item.Detail = "disabled by SKILL.md metadata"
				}
			}
			continue
		}
		if strings.HasPrefix(line, "#") && item.Name == fallbackName {
			if heading := strings.TrimSpace(strings.TrimLeft(line, "#")); heading != "" {
				item.Name = heading
			}
			continue
		}
		if item.Description == "" && line != "" && !strings.HasPrefix(line, "#") && !paragraphStarted {
			item.Description = line
			paragraphStarted = true
		}
	}
	if err := scanner.Err(); err != nil {
		return domain.Skill{}, err
	}
	return item, nil
}
