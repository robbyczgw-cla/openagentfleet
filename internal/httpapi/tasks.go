package httpapi

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/robbyczgw-cla/openagentfleet/internal/domain"
	"github.com/robbyczgw-cla/openagentfleet/internal/store"
)

type taskCursor struct {
	CreatedAt string `json:"created_at"`
	ID        string `json:"id"`
}

func (s *Server) prepareTaskDeliverables(runID string) string {
	relative := "outputs"
	if validTaskRunID.MatchString(runID) {
		relative = filepath.ToSlash(filepath.Join("outputs", runID))
	}
	if s.HarnessWorkdir != "" && relative != "outputs" {
		if workspace, err := os.OpenRoot(s.HarnessWorkdir); err == nil {
			defer workspace.Close()
			if err := workspace.MkdirAll("outputs", 0o700); err == nil {
				if info, err := workspace.Lstat("outputs"); err == nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0 {
					if err := workspace.MkdirAll(relative, 0o700); err == nil {
						if info, err := workspace.Lstat(relative); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
							return taskDeliverablesSystemPrompt(relative)
						}
					}
				}
			}
		}
	}
	return taskDeliverablesSystemPrompt(relative)
}

func taskDeliverablesSystemPrompt(relative string) string {
	return "Save user-facing deliverable files under " + relative + "/ in the workspace and link each file in the final response."
}

func (s *Server) handleTasks(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/tasks"), "/")
	if len(parts) == 1 && parts[0] == "" {
		f := store.TaskFilter{BotID: r.URL.Query().Get("bot_id"), Status: r.URL.Query().Get("status"), Query: r.URL.Query().Get("q")}
		if raw := r.URL.Query().Get("limit"); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil || n < 1 || n > 200 {
				s.writeErrorStatus(w, 400, errors.New("limit must be between 1 and 200"))
				return
			}
			f.Limit = n
		}
		if len(f.Query) > 500 {
			s.writeErrorStatus(w, 400, errors.New("query exceeds 500 bytes"))
			return
		}
		if raw := r.URL.Query().Get("cursor"); raw != "" {
			cursor, err := decodeTaskCursor(raw)
			if err != nil {
				s.writeErrorStatus(w, 400, errors.New("invalid task cursor"))
				return
			}
			f.BeforeCreatedAt, f.BeforeID = cursor.CreatedAt, cursor.ID
		}
		switch f.Status {
		case "", "queued", "running", "waiting_approval", "completed", "failed", "stopped", "blocked":
		default:
			s.writeErrorStatus(w, 400, errors.New("invalid task status"))
			return
		}
		items, hasMore, err := s.Store.ListTasks(r.Context(), f)
		if err != nil {
			s.writeError(w, err)
			return
		}
		response := map[string]any{"items": items}
		if hasMore && len(items) != 0 {
			response["next_cursor"] = encodeTaskCursor(taskCursor{CreatedAt: items[len(items)-1].CreatedAt, ID: items[len(items)-1].ID})
		}
		s.writeJSON(w, http.StatusOK, response)
		return
	}
	if len(parts) == 2 && parts[1] != "" {
		task, result, err := s.Store.GetTask(r.Context(), parts[1])
		if errors.Is(err, sql.ErrNoRows) {
			s.writeErrorStatus(w, 404, errors.New("task not found"))
			return
		}
		if err != nil {
			s.writeError(w, err)
			return
		}
		artifacts, err := s.Store.ListTaskArtifacts(r.Context(), parts[1])
		if err != nil {
			s.writeError(w, err)
			return
		}
		s.writeJSON(w, http.StatusOK, map[string]any{"task": task, "result": result, "artifacts": artifacts})
		return
	}
	if len(parts) == 5 && parts[2] == "artifacts" && (parts[4] == "content" || parts[4] == "download") {
		a, data, err := s.Store.GetTaskArtifact(r.Context(), parts[1], parts[3])
		if errors.Is(err, sql.ErrNoRows) {
			s.writeErrorStatus(w, 404, errors.New("artifact not found"))
			return
		}
		if err != nil {
			s.writeError(w, err)
			return
		}
		disposition := "attachment"
		mediaType := a.MediaType
		if parts[4] == "content" {
			if a.PreviewKind == "text" {
				mediaType = "text/plain; charset=utf-8"
				disposition = "inline"
			}
			if a.PreviewKind == "image" {
				disposition = "inline"
			}
		}
		w.Header().Set("Content-Type", mediaType)
		w.Header().Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": a.Name}))
		w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "private, no-store")
		http.ServeContent(w, r, a.Name, time.Time{}, bytes.NewReader(data))
		return
	}
	s.writeErrorStatus(w, 404, errors.New("task route not found"))
}

func encodeTaskCursor(cursor taskCursor) string {
	data, _ := json.Marshal(cursor)
	return base64.RawURLEncoding.EncodeToString(data)
}

func decodeTaskCursor(raw string) (taskCursor, error) {
	var cursor taskCursor
	if len(raw) > 2048 {
		return cursor, errors.New("cursor is too long")
	}
	data, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return cursor, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cursor); err != nil {
		return cursor, err
	}
	if cursor.CreatedAt == "" || cursor.ID == "" || decoder.Decode(&struct{}{}) != io.EOF {
		return taskCursor{}, errors.New("cursor fields are required")
	}
	return cursor, nil
}

var resultLinks = regexp.MustCompile(`\[[^\]\n]*\]\((<[^>\n]+>|[^\s)]+)\)`)
var validTaskRunID = regexp.MustCompile(`^run-[A-Za-z0-9_-]{1,128}$`)

// captureTaskArtifacts snapshots explicit deliverables before later runs can overwrite them.
// Legacy links directly under outputs/ remain supported, but their snapshot proves content at
// capture time, not which concurrent run authored the shared path.
func (s *Server) captureTaskArtifacts(run domain.Run, answer string) {
	if s.HarnessWorkdir == "" {
		return
	}
	workspace, err := os.OpenRoot(s.HarnessWorkdir)
	if err != nil {
		return
	}
	defer workspace.Close()
	info, err := workspace.Lstat("outputs")
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return
	}
	root, err := workspace.OpenRoot("outputs")
	if err != nil {
		return
	}
	defer root.Close()
	existing, err := s.Store.ListTaskArtifacts(context.Background(), run.ID)
	if err != nil {
		return
	}
	usedNames := make(map[string]bool, len(existing))
	for _, artifact := range existing {
		usedNames[strings.ToLower(artifact.Name)] = true
	}
	seenPaths := map[string]bool{}
	captured := 0
	for _, match := range resultLinks.FindAllStringSubmatch(answer, 100) {
		raw := strings.Trim(match[1], "<>")
		u, err := url.Parse(raw)
		if err != nil || (u.Scheme != "" && u.Scheme != "file") || u.Host != "" || u.RawQuery != "" || u.Fragment != "" {
			continue
		}
		path := u.Path
		if strings.HasPrefix(path, "/workspace/") {
			path = strings.TrimPrefix(path, "/workspace/")
		} else if filepath.IsAbs(path) {
			path, err = filepath.Rel(s.HarnessWorkdir, path)
			if err != nil {
				continue
			}
		}
		path = filepath.ToSlash(path)
		if !strings.HasPrefix(path, "outputs/") {
			continue
		}
		path = strings.TrimPrefix(path, "outputs/")
		valid := true
		current := ""
		for _, part := range strings.Split(path, "/") {
			if part == "" || strings.HasPrefix(part, ".") || strings.Contains(part, "\\") {
				valid = false
				break
			}
			current = filepath.Join(current, part)
			info, err := root.Lstat(current)
			if err != nil || info.Mode()&os.ModeSymlink != 0 {
				valid = false
				break
			}
		}
		if !valid || seenPaths[path] {
			continue
		}
		seenPaths[path] = true
		info, err = root.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		file, err := openTaskArtifactFile(root, path)
		if err != nil {
			continue
		}
		info, err = file.Stat()
		if err != nil || !info.Mode().IsRegular() || info.Size() > 10<<20 {
			file.Close()
			continue
		}
		data, err := io.ReadAll(io.LimitReader(file, (10<<20)+1))
		file.Close()
		if err != nil || len(data) > 10<<20 {
			continue
		}
		name := uniqueArtifactName(filepath.Base(path), usedNames)
		mediaType, preview := artifactMedia(name, data)
		if _, err := s.Store.SaveTaskArtifact(context.Background(), run.ID, name, mediaType, preview, data); err == nil {
			usedNames[strings.ToLower(name)] = true
			captured++
		}
		if len(existing)+captured >= 20 {
			break
		}
	}
}

func uniqueArtifactName(name string, used map[string]bool) string {
	if !used[strings.ToLower(name)] {
		return name
	}
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	for suffix := 2; ; suffix++ {
		candidate := fmt.Sprintf("%s (%d)%s", stem, suffix, ext)
		if !used[strings.ToLower(candidate)] {
			return candidate
		}
	}
}

func artifactMedia(name string, data []byte) (string, string) {
	detected := http.DetectContentType(data)
	switch detected {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		return detected, "image"
	}
	ext := strings.ToLower(filepath.Ext(name))
	switch ext {
	case ".txt", ".md", ".csv", ".json", ".log", ".yaml", ".yml", ".xml":
		return "text/plain; charset=utf-8", "text"
	}
	mediaType := mime.TypeByExtension(ext)
	if mediaType == "" {
		mediaType = "application/octet-stream"
	}
	return mediaType, "download"
}
