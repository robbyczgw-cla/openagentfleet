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
	"sync"
	"time"

	"github.com/robbyczgw-cla/openagentfleet/internal/domain"
	"github.com/robbyczgw-cla/openagentfleet/internal/id"
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
	workdir, err := s.runWorkdir(context.Background(), runID)
	if err != nil {
		return taskDeliverablesSystemPrompt(relative)
	}
	if workdir != "" && relative != "outputs" {
		if workspace, err := os.OpenRoot(workdir); err == nil {
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
	if len(parts) == 3 && parts[1] != "" && (parts[2] == "retry" || parts[2] == "revise") {
		if r.Method != http.MethodPost {
			s.writeErrorStatus(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
			return
		}
		s.createTaskFollowup(w, r, parts[1], parts[2])
		return
	}
	if len(parts) == 1 && parts[0] == "" {
		if r.Method != http.MethodGet {
			s.writeErrorStatus(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
			return
		}
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
		if r.Method != http.MethodGet {
			s.writeErrorStatus(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
			return
		}
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
		input, err := s.Store.GetTaskInput(r.Context(), parts[1])
		if err != nil {
			s.writeError(w, err)
			return
		}
		s.writeJSON(w, http.StatusOK, map[string]any{"task": task, "input": input, "result": result, "artifacts": artifacts})
		return
	}
	if len(parts) == 5 && parts[2] == "artifacts" && (parts[4] == "content" || parts[4] == "download") {
		if r.Method != http.MethodGet {
			s.writeErrorStatus(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
			return
		}
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

type taskFollowupRequest struct {
	Brief         string   `json:"brief"`
	AgentID       string   `json:"agent_id"`
	AttachmentIDs []string `json:"attachment_ids"`
}

var taskFollowupRequestMu sync.Mutex

func (s *Server) createTaskFollowup(w http.ResponseWriter, r *http.Request, parentTaskID, kind string) {
	taskFollowupRequestMu.Lock()
	defer taskFollowupRequestMu.Unlock()
	idempotencyKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if idempotencyKey == "" {
		s.writeErrorStatus(w, http.StatusBadRequest, errors.New("Idempotency-Key is required"))
		return
	}
	var request taskFollowupRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		s.writeErrorStatus(w, http.StatusBadRequest, err)
		return
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		s.writeErrorStatus(w, http.StatusBadRequest, errors.New("request body must contain one JSON object"))
		return
	}
	task, _, err := s.Store.GetTask(r.Context(), parentTaskID)
	if errors.Is(err, sql.ErrNoRows) {
		s.writeErrorStatus(w, http.StatusNotFound, errors.New("task not found"))
		return
	}
	if err != nil {
		s.writeError(w, err)
		return
	}
	input, err := s.Store.GetTaskInput(r.Context(), parentTaskID)
	if err != nil {
		s.writeError(w, err)
		return
	}
	parentWorkdir, err := s.Store.GetRunWorkdir(r.Context(), parentTaskID)
	if err != nil {
		s.writeError(w, err)
		return
	}
	request.AgentID = strings.TrimSpace(request.AgentID)
	if request.AgentID == "" {
		s.writeErrorStatus(w, http.StatusBadRequest, errors.New("agent_id is required"))
		return
	}
	if request.AgentID != task.BotID {
		s.writeErrorStatus(w, http.StatusBadRequest, errors.New("agent_id must match the original task for this release"))
		return
	}
	brief := input.Brief
	if kind == "revise" {
		brief = strings.TrimSpace(request.Brief)
		if brief == "" {
			s.writeErrorStatus(w, http.StatusBadRequest, errors.New("brief is required"))
			return
		}
	} else if strings.TrimSpace(request.Brief) != "" && strings.TrimSpace(request.Brief) != input.Brief {
		s.writeErrorStatus(w, http.StatusBadRequest, errors.New("retry uses the original brief"))
		return
	}
	if len(request.AttachmentIDs) > 10 {
		s.writeErrorStatus(w, http.StatusBadRequest, errors.New("at most 10 attachments are allowed"))
		return
	}
	existing, found, err := s.Store.FindTaskFollowup(r.Context(), store.CreateTaskFollowupInput{
		ParentTaskID: parentTaskID, Kind: kind, IdempotencyKey: idempotencyKey,
		BotID: request.AgentID, Content: brief, SourceAttachmentIDs: request.AttachmentIDs,
	})
	if err != nil {
		if errors.Is(err, store.ErrTaskFollowupConflict) {
			s.writeErrorStatus(w, http.StatusConflict, err)
		} else {
			s.writeError(w, err)
		}
		return
	}
	if found {
		s.writeJSON(w, http.StatusAccepted, map[string]any{"message": existing.Message, "run": existing.Run})
		return
	}
	copies, err := s.copyTaskInputAttachments(r.Context(), task.ConversationID, input.Attachments, request.AttachmentIDs)
	if err != nil {
		s.writeErrorStatus(w, http.StatusBadRequest, err)
		return
	}
	defer s.cleanupPendingTaskAttachmentCopies(copies)
	copyIDs := make([]string, len(copies))
	for index := range copies {
		copyIDs[index] = copies[index].ID
	}
	message := messageRequest{ConversationID: task.ConversationID, Content: brief, AttachmentIDs: copyIDs}
	s.dispatchMessage(w, r, message, messageDispatchOptions{
		FreshSession: true,
		SnapshotProject: func(ctx context.Context, run domain.Run) error {
			return s.Store.SnapshotTaskFollowupProject(ctx, parentTaskID, run.ID, run.BotID)
		},
		AfterRunCreated: func(ctx context.Context, run domain.Run) error {
			if parentWorkdir == "" {
				return nil
			}
			return s.Store.SetRunWorkdir(ctx, run.ID, parentWorkdir)
		},
		CreateErrorCode: func(err error) int {
			switch {
			case errors.Is(err, store.ErrTaskFollowupConflict), errors.Is(err, store.ErrTaskFollowupState), errors.Is(err, store.ErrTaskAttemptLimit):
				return http.StatusConflict
			default:
				return http.StatusInternalServerError
			}
		},
		CreateRun: func(ctx context.Context, resolved resolvedMessageRunInput) (messageRunResult, error) {
			created, err := s.Store.CreateTaskFollowup(ctx, store.CreateTaskFollowupInput{
				ParentTaskID:        parentTaskID,
				Kind:                kind,
				IdempotencyKey:      idempotencyKey,
				BotID:               resolved.BotID,
				Provider:            resolved.Provider,
				Content:             resolved.Content,
				Prompt:              resolved.Prompt,
				AttachmentIDs:       resolved.AttachmentIDs,
				SourceAttachmentIDs: request.AttachmentIDs,
			})
			return messageRunResult{Message: created.Message, Attachments: created.Attachments, Run: created.Run, QueuedEvent: created.QueuedEvent, Created: created.Created}, err
		},
	})
}

func (s *Server) copyTaskInputAttachments(ctx context.Context, conversationID string, offered []domain.Attachment, selectedIDs []string) ([]domain.Attachment, error) {
	byID := make(map[string]domain.Attachment, len(offered))
	for _, attachment := range offered {
		byID[attachment.ID] = attachment
	}
	uploadDir := s.UploadDir
	if uploadDir == "" {
		uploadDir = filepath.Join(s.HarnessWorkdir, ".openagentfleet", "uploads")
	}
	if len(selectedIDs) > 0 {
		if err := os.MkdirAll(uploadDir, 0o700); err != nil {
			return nil, err
		}
	}
	copies := make([]domain.Attachment, 0, len(selectedIDs))
	seen := make(map[string]bool, len(selectedIDs))
	for _, sourceID := range selectedIDs {
		if seen[sourceID] {
			s.cleanupPendingTaskAttachmentCopies(copies)
			return nil, errors.New("attachment was supplied more than once")
		}
		seen[sourceID] = true
		source, ok := byID[sourceID]
		if !ok {
			s.cleanupPendingTaskAttachmentCopies(copies)
			return nil, errors.New("attachment does not belong to the original task")
		}
		copyID := id.New("file")
		target := filepath.Join(uploadDir, copyID+"-"+safeAttachmentName(source.Name))
		if err := copyTaskAttachmentFile(source.StoragePath, target); err != nil {
			s.cleanupPendingTaskAttachmentCopies(copies)
			return nil, err
		}
		copied, err := s.Store.CreateAttachment(ctx, domain.Attachment{ID: copyID, ConversationID: conversationID, Name: source.Name, MediaType: source.MediaType, Size: source.Size, StoragePath: target})
		if err != nil {
			_ = os.Remove(target)
			s.cleanupPendingTaskAttachmentCopies(copies)
			return nil, err
		}
		copies = append(copies, copied)
	}
	return copies, nil
}

func copyTaskAttachmentFile(sourcePath, targetPath string) error {
	sourceRoot, err := os.OpenRoot(filepath.Dir(sourcePath))
	if err != nil {
		return fmt.Errorf("open original attachment directory: %w", err)
	}
	defer sourceRoot.Close()
	name := filepath.Base(sourcePath)
	info, err := sourceRoot.Lstat(name)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("original attachment content is unavailable")
	}
	source, err := openTaskArtifactFile(sourceRoot, name)
	if err != nil {
		return fmt.Errorf("open original attachment: %w", err)
	}
	defer source.Close()
	openedInfo, err := source.Stat()
	if err != nil || !openedInfo.Mode().IsRegular() || !os.SameFile(info, openedInfo) {
		return errors.New("original attachment changed while opening")
	}
	target, err := os.OpenFile(targetPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	written, copyErr := io.Copy(target, io.LimitReader(source, maxAttachmentBytes+1))
	closeErr := target.Close()
	if copyErr != nil || closeErr != nil || written > maxAttachmentBytes {
		_ = os.Remove(targetPath)
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		return errors.New("original attachment exceeds the attachment limit")
	}
	return nil
}

func (s *Server) cleanupPendingTaskAttachmentCopies(copies []domain.Attachment) {
	for _, attachment := range copies {
		deleted, err := s.Store.DeletePendingAttachment(context.Background(), attachment.ID)
		if err == nil {
			_ = os.Remove(deleted.StoragePath)
		}
	}
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
	workdir, err := s.runWorkdir(context.Background(), run.ID)
	if err != nil || workdir == "" {
		return
	}
	workspace, err := os.OpenRoot(workdir)
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
			path, err = filepath.Rel(workdir, path)
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
