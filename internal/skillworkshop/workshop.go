// Package skillworkshop manages reviewable, locally-owned skill drafts.
//
// It deliberately does not execute skills. Its only responsibility is to make
// the path from a captured task to an enabled, portable SKILL.md auditable:
// Draft -> Reviewed -> Tested -> Enabled. Every enabled version is copied to
// a new directory and is never overwritten or deleted by this package.
package skillworkshop

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	maxTextBytes = 32 * 1024
	maxFileBytes = 256 * 1024
)

var (
	// ErrInvalidID means an ID cannot safely be used as a directory name.
	ErrInvalidID = errors.New("invalid skill draft ID")
	// ErrInvalidInput means required metadata is missing or malformed.
	ErrInvalidInput = errors.New("invalid skill draft input")
	// ErrPotentialSecret keeps credentials out of draft metadata and evidence.
	ErrPotentialSecret = errors.New("potential secret is not allowed in a skill draft")
	// ErrInvalidTransition means an operation does not fit the current lifecycle state.
	ErrInvalidTransition = errors.New("invalid skill draft state transition")
	// ErrContentChanged means the on-disk content no longer matches its recorded hash.
	ErrContentChanged = errors.New("skill draft content changed after it was recorded")
	// ErrNotFound means a locally-owned draft or enabled version does not exist.
	ErrNotFound = errors.New("skill draft not found")
	// ErrUnsafeReview means an enable attempt has no approved review for this content.
	ErrUnsafeReview = errors.New("skill draft needs an approved security review")
	// ErrUnsafeTest means an enable attempt has no passing safe test for this content.
	ErrUnsafeTest = errors.New("skill draft needs a passing safe test")
)

// State is the current lifecycle state of one draft revision.
type State string

const (
	StateDraft    State = "draft"
	StateReviewed State = "reviewed"
	StateTested   State = "tested"
	StateEnabled  State = "enabled"
)

// DraftInput is intentionally limited to non-secret, human-readable context.
// ID is optional; when omitted, a stable safe ID is derived from Name.
type DraftInput struct {
	ID          string
	Name        string
	Description string
	SourceTask  string
}

// Draft is durable metadata for the current revision of a local skill draft.
type Draft struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	Description   string    `json:"description"`
	SourceTask    string    `json:"source_task"`
	State         State     `json:"state"`
	Revision      int       `json:"revision"`
	ContentSHA256 string    `json:"content_sha256"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// SecurityReviewInput records a human review of the exact current revision.
// A rejected review is retained but leaves the revision in Draft state.
type SecurityReviewInput struct {
	Reviewer string
	Approved bool
	Findings []string
	Notes    string
}

// SecurityReview is persisted alongside the reviewed revision.
type SecurityReview struct {
	Reviewer      string    `json:"reviewer"`
	Approved      bool      `json:"approved"`
	Findings      []string  `json:"findings,omitempty"`
	Notes         string    `json:"notes,omitempty"`
	ContentSHA256 string    `json:"content_sha256"`
	ReviewedAt    time.Time `json:"reviewed_at"`
}

// SafeTestInput records a safe, deliberately bounded validation run.
// A failed test is retained but leaves the revision in Reviewed state.
type SafeTestInput struct {
	Runner   string
	Passed   bool
	Evidence string
}

// SafeTest is persisted alongside the tested revision.
type SafeTest struct {
	Runner        string    `json:"runner"`
	Passed        bool      `json:"passed"`
	Evidence      string    `json:"evidence,omitempty"`
	ContentSHA256 string    `json:"content_sha256"`
	TestedAt      time.Time `json:"tested_at"`
}

// Inspection returns a draft together with its current revision artifacts.
type Inspection struct {
	Draft    Draft
	Proposal string
	Skill    string
	Review   *SecurityReview
	SafeTest *SafeTest
}

// Deployment describes the mutable active pointer for immutable enabled skill
// versions. Disabling a deployment only changes this pointer.
type Deployment struct {
	DraftID       string    `json:"draft_id"`
	Active        bool      `json:"active"`
	Version       int       `json:"version"`
	ContentSHA256 string    `json:"content_sha256"`
	ChangedAt     time.Time `json:"changed_at"`
}

type enabledVersion struct {
	DraftID       string    `json:"draft_id"`
	Version       int       `json:"version"`
	Revision      int       `json:"revision"`
	Name          string    `json:"name"`
	ContentSHA256 string    `json:"content_sha256"`
	SkillSHA256   string    `json:"skill_sha256"`
	EnabledAt     time.Time `json:"enabled_at"`
}

// Workshop owns a single local draft root. It serializes operations within a
// process; callers that share a root across processes should externally
// serialize writes.
type Workshop struct {
	root string
	now  func() time.Time
	mu   sync.Mutex
}

// New opens (or creates) a locally-owned draft root.
func New(root string) (*Workshop, error) {
	resolved, err := normalizeRoot(root, 0o700)
	if err != nil {
		return nil, err
	}
	return &Workshop{root: resolved, now: time.Now}, nil
}

// Root is the resolved, locally-owned draft root.
func (w *Workshop) Root() string {
	return w.root
}

// Create creates a first draft revision and generated proposal/SKILL.md files.
func (w *Workshop) Create(input DraftInput) (Draft, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	input, err := normalizeInput(input, "")
	if err != nil {
		return Draft{}, err
	}
	dir, err := w.draftDir(input.ID)
	if err != nil {
		return Draft{}, err
	}
	if _, err := os.Lstat(dir); err == nil {
		return Draft{}, fmt.Errorf("%w: draft %q already exists", ErrInvalidInput, input.ID)
	} else if !errors.Is(err, os.ErrNotExist) {
		return Draft{}, err
	}
	if err := os.Mkdir(dir, 0o700); err != nil {
		return Draft{}, err
	}

	now := w.now().UTC()
	draft := Draft{
		ID:          input.ID,
		Name:        input.Name,
		Description: input.Description,
		SourceTask:  input.SourceTask,
		State:       StateDraft,
		Revision:    1,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := w.writeRevision(dir, draft.Revision, input); err != nil {
		return Draft{}, err
	}
	hash, err := w.contentHash(dir, draft)
	if err != nil {
		return Draft{}, err
	}
	draft.ContentSHA256 = hash
	if err := writeJSON(filepath.Join(dir, "metadata.json"), draft, 0o600); err != nil {
		return Draft{}, err
	}
	return draft, nil
}

// Revise creates a new draft revision. Previous review/test records and
// immutable enabled versions remain intact, while the new revision starts at
// Draft and must repeat review and safe testing.
func (w *Workshop) Revise(id string, input DraftInput) (Draft, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	draft, dir, err := w.loadDraft(id)
	if err != nil {
		return Draft{}, err
	}
	input, err = normalizeInput(input, draft.ID)
	if err != nil {
		return Draft{}, err
	}
	if err := w.verifyContent(dir, draft); err != nil {
		return Draft{}, err
	}

	draft.Name = input.Name
	draft.Description = input.Description
	draft.SourceTask = input.SourceTask
	draft.State = StateDraft
	draft.Revision++
	draft.UpdatedAt = w.now().UTC()
	if err := w.writeRevision(dir, draft.Revision, input); err != nil {
		return Draft{}, err
	}
	hash, err := w.contentHash(dir, draft)
	if err != nil {
		return Draft{}, err
	}
	draft.ContentSHA256 = hash
	if err := writeJSON(filepath.Join(dir, "metadata.json"), draft, 0o600); err != nil {
		return Draft{}, err
	}
	return draft, nil
}

// Inspect reads one verified draft revision and its optional review/test data.
func (w *Workshop) Inspect(id string) (Inspection, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	draft, dir, err := w.loadDraft(id)
	if err != nil {
		return Inspection{}, err
	}
	proposal, skill, err := w.readRevision(dir, draft.Revision)
	if err != nil {
		return Inspection{}, err
	}
	if err := w.verifyContentWithFiles(draft, proposal, skill); err != nil {
		return Inspection{}, err
	}
	inspection := Inspection{Draft: draft, Proposal: proposal, Skill: skill}
	if review, err := readOptionalJSON[SecurityReview](reviewPath(dir, draft.Revision)); err != nil {
		return Inspection{}, err
	} else {
		inspection.Review = review
	}
	if safeTest, err := readOptionalJSON[SafeTest](safeTestPath(dir, draft.Revision)); err != nil {
		return Inspection{}, err
	} else {
		inspection.SafeTest = safeTest
	}
	return inspection, nil
}

// List returns verified metadata ordered by stable draft ID.
func (w *Workshop) List() ([]Draft, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	entries, err := os.ReadDir(w.root)
	if err != nil {
		return nil, err
	}
	items := make([]Draft, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if err := validateID(entry.Name()); err != nil {
			return nil, fmt.Errorf("%w: unexpected directory %q", ErrInvalidID, entry.Name())
		}
		draft, dir, err := w.loadDraft(entry.Name())
		if err != nil {
			return nil, err
		}
		if err := w.verifyContent(dir, draft); err != nil {
			return nil, err
		}
		items = append(items, draft)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return items, nil
}

// RecordSecurityReview records the review for the exact current content. Only
// an approved review advances Draft -> Reviewed.
func (w *Workshop) RecordSecurityReview(id string, input SecurityReviewInput) (Draft, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	draft, dir, err := w.loadDraft(id)
	if err != nil {
		return Draft{}, err
	}
	if draft.State != StateDraft {
		return Draft{}, transitionError(draft.State, "record a security review")
	}
	if err := w.verifyContent(dir, draft); err != nil {
		return Draft{}, err
	}
	input, err = normalizeReview(input)
	if err != nil {
		return Draft{}, err
	}
	review := SecurityReview{
		Reviewer:      input.Reviewer,
		Approved:      input.Approved,
		Findings:      input.Findings,
		Notes:         input.Notes,
		ContentSHA256: draft.ContentSHA256,
		ReviewedAt:    w.now().UTC(),
	}
	if err := ensureDir(filepath.Join(dir, "reviews"), 0o700); err != nil {
		return Draft{}, err
	}
	if err := writeJSON(reviewPath(dir, draft.Revision), review, 0o600); err != nil {
		return Draft{}, err
	}
	if review.Approved {
		draft.State = StateReviewed
		draft.UpdatedAt = w.now().UTC()
		if err := writeJSON(filepath.Join(dir, "metadata.json"), draft, 0o600); err != nil {
			return Draft{}, err
		}
	}
	return draft, nil
}

// MarkSafeTest records a bounded test. Only a passing test advances
// Reviewed -> Tested.
func (w *Workshop) MarkSafeTest(id string, input SafeTestInput) (Draft, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	draft, dir, err := w.loadDraft(id)
	if err != nil {
		return Draft{}, err
	}
	if draft.State != StateReviewed {
		return Draft{}, transitionError(draft.State, "record a safe test")
	}
	if err := w.verifyContent(dir, draft); err != nil {
		return Draft{}, err
	}
	review, err := readRequiredJSON[SecurityReview](reviewPath(dir, draft.Revision))
	if err != nil {
		return Draft{}, err
	}
	if !review.Approved || review.ContentSHA256 != draft.ContentSHA256 {
		return Draft{}, ErrUnsafeReview
	}
	input, err = normalizeSafeTest(input)
	if err != nil {
		return Draft{}, err
	}
	safeTest := SafeTest{
		Runner:        input.Runner,
		Passed:        input.Passed,
		Evidence:      input.Evidence,
		ContentSHA256: draft.ContentSHA256,
		TestedAt:      w.now().UTC(),
	}
	if err := ensureDir(filepath.Join(dir, "tests"), 0o700); err != nil {
		return Draft{}, err
	}
	if err := writeJSON(safeTestPath(dir, draft.Revision), safeTest, 0o600); err != nil {
		return Draft{}, err
	}
	if safeTest.Passed {
		draft.State = StateTested
		draft.UpdatedAt = w.now().UTC()
		if err := writeJSON(filepath.Join(dir, "metadata.json"), draft, 0o600); err != nil {
			return Draft{}, err
		}
	}
	return draft, nil
}

// Enable copies the tested SKILL.md to a fresh immutable version under
// enabledRoot and updates its active pointer. It never overwrites a version.
func (w *Workshop) Enable(id, enabledRoot string) (Deployment, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	draft, draftDir, err := w.loadDraft(id)
	if err != nil {
		return Deployment{}, err
	}
	if draft.State != StateTested {
		return Deployment{}, transitionError(draft.State, "enable the draft")
	}
	proposal, skill, err := w.readRevision(draftDir, draft.Revision)
	if err != nil {
		return Deployment{}, err
	}
	if err := w.verifyContentWithFiles(draft, proposal, skill); err != nil {
		return Deployment{}, err
	}
	review, err := readRequiredJSON[SecurityReview](reviewPath(draftDir, draft.Revision))
	if err != nil {
		return Deployment{}, err
	}
	if !review.Approved || review.ContentSHA256 != draft.ContentSHA256 {
		return Deployment{}, ErrUnsafeReview
	}
	safeTest, err := readRequiredJSON[SafeTest](safeTestPath(draftDir, draft.Revision))
	if err != nil {
		return Deployment{}, err
	}
	if !safeTest.Passed || safeTest.ContentSHA256 != draft.ContentSHA256 {
		return Deployment{}, ErrUnsafeTest
	}

	root, err := normalizeRoot(enabledRoot, 0o755)
	if err != nil {
		return Deployment{}, err
	}
	deploymentDir, err := childDir(root, draft.ID)
	if err != nil {
		return Deployment{}, err
	}
	if err := ensureDir(deploymentDir, 0o755); err != nil {
		return Deployment{}, err
	}
	versionsDir := filepath.Join(deploymentDir, "versions")
	if err := ensureDir(versionsDir, 0o755); err != nil {
		return Deployment{}, err
	}
	version, err := nextVersion(versionsDir)
	if err != nil {
		return Deployment{}, err
	}
	versionDir := filepath.Join(versionsDir, versionName(version))
	if err := os.Mkdir(versionDir, 0o755); err != nil {
		return Deployment{}, err
	}
	versionComplete := false
	defer func() {
		if !versionComplete {
			_ = os.RemoveAll(versionDir)
		}
	}()
	skillPath := filepath.Join(versionDir, "SKILL.md")
	if err := writeNewFile(skillPath, []byte(skill), 0o444); err != nil {
		return Deployment{}, err
	}
	if err := os.Chmod(skillPath, 0o444); err != nil {
		return Deployment{}, err
	}
	manifest := enabledVersion{
		DraftID:       draft.ID,
		Version:       version,
		Revision:      draft.Revision,
		Name:          draft.Name,
		ContentSHA256: draft.ContentSHA256,
		SkillSHA256:   hashText(skill),
		EnabledAt:     w.now().UTC(),
	}
	if err := writeNewJSON(filepath.Join(versionDir, "manifest.json"), manifest, 0o444); err != nil {
		return Deployment{}, err
	}
	if err := os.Chmod(filepath.Join(versionDir, "manifest.json"), 0o444); err != nil {
		return Deployment{}, err
	}
	versionComplete = true

	deployment := Deployment{
		DraftID:       draft.ID,
		Active:        true,
		Version:       version,
		ContentSHA256: draft.ContentSHA256,
		ChangedAt:     w.now().UTC(),
	}
	if err := writeJSON(filepath.Join(deploymentDir, "active.json"), deployment, 0o600); err != nil {
		return Deployment{}, err
	}
	draft.State = StateEnabled
	draft.UpdatedAt = w.now().UTC()
	if err := writeJSON(filepath.Join(draftDir, "metadata.json"), draft, 0o600); err != nil {
		return Deployment{}, err
	}
	return deployment, nil
}

// Deployment reads the active pointer after validating its immutable version.
func (w *Workshop) Deployment(id, enabledRoot string) (Deployment, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.deployment(id, enabledRoot)
}

// Disable makes an enabled skill inactive without removing any version.
func (w *Workshop) Disable(id, enabledRoot string) (Deployment, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	deployment, dir, err := w.loadDeployment(id, enabledRoot)
	if err != nil {
		return Deployment{}, err
	}
	if !deployment.Active {
		return deployment, nil
	}
	deployment.Active = false
	deployment.ChangedAt = w.now().UTC()
	if err := writeJSON(filepath.Join(dir, "active.json"), deployment, 0o600); err != nil {
		return Deployment{}, err
	}
	return deployment, nil
}

// Rollback switches the active pointer to a retained immutable version. It
// does not delete, modify, or recreate any historical version.
func (w *Workshop) Rollback(id, enabledRoot string, version int) (Deployment, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if err := validateID(id); err != nil {
		return Deployment{}, err
	}
	if version < 1 {
		return Deployment{}, fmt.Errorf("%w: version must be positive", ErrInvalidInput)
	}
	root, err := normalizeRoot(enabledRoot, 0o755)
	if err != nil {
		return Deployment{}, err
	}
	dir, err := childDir(root, id)
	if err != nil {
		return Deployment{}, err
	}
	if err := assertDir(dir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Deployment{}, ErrNotFound
		}
		return Deployment{}, err
	}
	manifest, err := readEnabledVersion(filepath.Join(dir, "versions", versionName(version)))
	if err != nil {
		return Deployment{}, err
	}
	if manifest.DraftID != id || manifest.Version != version {
		return Deployment{}, ErrContentChanged
	}
	deployment := Deployment{
		DraftID:       id,
		Active:        true,
		Version:       version,
		ContentSHA256: manifest.ContentSHA256,
		ChangedAt:     w.now().UTC(),
	}
	if err := writeJSON(filepath.Join(dir, "active.json"), deployment, 0o600); err != nil {
		return Deployment{}, err
	}
	return deployment, nil
}

func (w *Workshop) deployment(id, enabledRoot string) (Deployment, error) {
	deployment, _, err := w.loadDeployment(id, enabledRoot)
	return deployment, err
}

func (w *Workshop) loadDeployment(id, enabledRoot string) (Deployment, string, error) {
	if err := validateID(id); err != nil {
		return Deployment{}, "", err
	}
	root, err := normalizeRoot(enabledRoot, 0o755)
	if err != nil {
		return Deployment{}, "", err
	}
	dir, err := childDir(root, id)
	if err != nil {
		return Deployment{}, "", err
	}
	if err := assertDir(dir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Deployment{}, "", ErrNotFound
		}
		return Deployment{}, "", err
	}
	deployment, err := readRequiredJSON[Deployment](filepath.Join(dir, "active.json"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Deployment{}, "", ErrNotFound
		}
		return Deployment{}, "", err
	}
	if deployment.DraftID != id || deployment.Version < 1 {
		return Deployment{}, "", fmt.Errorf("%w: invalid deployment metadata", ErrContentChanged)
	}
	manifest, err := readEnabledVersion(filepath.Join(dir, "versions", versionName(deployment.Version)))
	if err != nil {
		return Deployment{}, "", err
	}
	if manifest.DraftID != id || manifest.Version != deployment.Version || manifest.ContentSHA256 != deployment.ContentSHA256 {
		return Deployment{}, "", ErrContentChanged
	}
	return deployment, dir, nil
}

func (w *Workshop) loadDraft(id string) (Draft, string, error) {
	dir, err := w.draftDir(id)
	if err != nil {
		return Draft{}, "", err
	}
	if err := assertDir(dir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Draft{}, "", ErrNotFound
		}
		return Draft{}, "", err
	}
	draft, err := readRequiredJSON[Draft](filepath.Join(dir, "metadata.json"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Draft{}, "", ErrNotFound
		}
		return Draft{}, "", err
	}
	if draft.ID != id || draft.Revision < 1 || !validState(draft.State) || len(draft.ContentSHA256) != sha256.Size*2 {
		return Draft{}, "", fmt.Errorf("%w: invalid draft metadata", ErrContentChanged)
	}
	return draft, dir, nil
}

func (w *Workshop) draftDir(id string) (string, error) {
	if err := validateID(id); err != nil {
		return "", err
	}
	return childDir(w.root, id)
}

func (w *Workshop) writeRevision(dir string, revision int, input DraftInput) error {
	if revision < 1 {
		return fmt.Errorf("%w: revision must be positive", ErrInvalidInput)
	}
	if err := ensureDir(filepath.Join(dir, "revisions"), 0o700); err != nil {
		return err
	}
	revisionDir := filepath.Join(dir, "revisions", revisionName(revision))
	if err := os.Mkdir(revisionDir, 0o700); err != nil {
		return err
	}
	revisionComplete := false
	defer func() {
		if !revisionComplete {
			_ = os.RemoveAll(revisionDir)
		}
	}()
	if err := writeNewFile(filepath.Join(revisionDir, "PROPOSAL.md"), []byte(proposalMarkdown(input)), 0o600); err != nil {
		return err
	}
	if err := writeNewFile(filepath.Join(revisionDir, "SKILL.md"), []byte(skillMarkdown(input)), 0o600); err != nil {
		return err
	}
	revisionComplete = true
	return nil
}

func (w *Workshop) readRevision(dir string, revision int) (string, string, error) {
	base := filepath.Join(dir, "revisions", revisionName(revision))
	if err := assertDir(base); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", "", ErrNotFound
		}
		return "", "", err
	}
	proposal, err := readRegularFile(filepath.Join(base, "PROPOSAL.md"), maxFileBytes)
	if err != nil {
		return "", "", err
	}
	skill, err := readRegularFile(filepath.Join(base, "SKILL.md"), maxFileBytes)
	if err != nil {
		return "", "", err
	}
	return string(proposal), string(skill), nil
}

func (w *Workshop) contentHash(dir string, draft Draft) (string, error) {
	proposal, skill, err := w.readRevision(dir, draft.Revision)
	if err != nil {
		return "", err
	}
	return contentHash(draft, proposal, skill), nil
}

func (w *Workshop) verifyContent(dir string, draft Draft) error {
	proposal, skill, err := w.readRevision(dir, draft.Revision)
	if err != nil {
		return err
	}
	return w.verifyContentWithFiles(draft, proposal, skill)
}

func (w *Workshop) verifyContentWithFiles(draft Draft, proposal, skill string) error {
	if contentHash(draft, proposal, skill) != draft.ContentSHA256 {
		return ErrContentChanged
	}
	return nil
}

func normalizeRoot(root string, perm os.FileMode) (string, error) {
	if strings.TrimSpace(root) == "" {
		return "", fmt.Errorf("%w: root is required", ErrInvalidInput)
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(abs, perm); err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	if err := assertDir(resolved); err != nil {
		return "", err
	}
	return resolved, nil
}

func childDir(root, name string) (string, error) {
	if err := validateID(name); err != nil {
		return "", err
	}
	child := filepath.Join(root, name)
	rel, err := filepath.Rel(root, child)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", ErrInvalidID
	}
	return child, nil
}

func ensureDir(path string, perm os.FileMode) error {
	if err := assertDir(path); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Mkdir(path, perm); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	return assertDir(path)
}

func assertDir(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("%w: %s is not a regular directory", ErrInvalidInput, path)
	}
	return nil
}

func readRegularFile(path string, limit int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: %s is not a regular file", ErrInvalidInput, path)
	}
	if info.Size() > limit {
		return nil, fmt.Errorf("%w: %s exceeds %d bytes", ErrInvalidInput, path, limit)
	}
	return os.ReadFile(path)
}

func writeJSON(path string, value any, perm os.FileMode) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(path, append(data, '\n'), perm)
}

func writeNewJSON(path string, value any, perm os.FileMode) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return writeNewFile(path, append(data, '\n'), perm)
}

func readRequiredJSON[T any](path string) (T, error) {
	var value T
	data, err := readRegularFile(path, maxFileBytes)
	if err != nil {
		return value, err
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return value, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return value, fmt.Errorf("%w: extra JSON value", ErrInvalidInput)
		}
		return value, err
	}
	return value, nil
}

func readOptionalJSON[T any](path string) (*T, error) {
	value, err := readRequiredJSON[T](path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &value, nil
}

func atomicWrite(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := assertDir(dir); err != nil {
		return err
	}
	file, err := os.CreateTemp(dir, ".openagentfleet-tmp-")
	if err != nil {
		return err
	}
	temp := file.Name()
	defer os.Remove(temp)
	if err := file.Chmod(perm); err != nil {
		file.Close()
		return err
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(temp, path)
}

func writeNewFile(path string, data []byte, perm os.FileMode) error {
	if err := assertDir(filepath.Dir(path)); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}

func normalizeInput(input DraftInput, expectedID string) (DraftInput, error) {
	input.ID = strings.TrimSpace(input.ID)
	if expectedID != "" {
		if input.ID != "" && input.ID != expectedID {
			return DraftInput{}, fmt.Errorf("%w: revision ID cannot change", ErrInvalidInput)
		}
		input.ID = expectedID
	} else if input.ID == "" {
		input.ID = slugify(input.Name)
	}
	if err := validateID(input.ID); err != nil {
		return DraftInput{}, err
	}
	var err error
	if input.Name, err = normalizeText("name", input.Name, true, true); err != nil {
		return DraftInput{}, err
	}
	if input.Description, err = normalizeText("description", input.Description, true, false); err != nil {
		return DraftInput{}, err
	}
	if input.SourceTask, err = normalizeText("source task", input.SourceTask, true, false); err != nil {
		return DraftInput{}, err
	}
	return input, nil
}

func normalizeReview(input SecurityReviewInput) (SecurityReviewInput, error) {
	var err error
	if input.Reviewer, err = normalizeText("reviewer", input.Reviewer, true, true); err != nil {
		return SecurityReviewInput{}, err
	}
	if input.Notes, err = normalizeText("review notes", input.Notes, false, false); err != nil {
		return SecurityReviewInput{}, err
	}
	findings := make([]string, 0, len(input.Findings))
	for _, finding := range input.Findings {
		finding, err = normalizeText("review finding", finding, true, false)
		if err != nil {
			return SecurityReviewInput{}, err
		}
		findings = append(findings, finding)
	}
	input.Findings = findings
	return input, nil
}

func normalizeSafeTest(input SafeTestInput) (SafeTestInput, error) {
	var err error
	if input.Runner, err = normalizeText("test runner", input.Runner, true, true); err != nil {
		return SafeTestInput{}, err
	}
	if input.Evidence, err = normalizeText("test evidence", input.Evidence, false, false); err != nil {
		return SafeTestInput{}, err
	}
	return input, nil
}

func normalizeText(field, value string, required, oneLine bool) (string, error) {
	value = strings.TrimSpace(value)
	if required && value == "" {
		return "", fmt.Errorf("%w: %s is required", ErrInvalidInput, field)
	}
	if len(value) > maxTextBytes || strings.ContainsRune(value, '\x00') {
		return "", fmt.Errorf("%w: %s is too large or contains a NUL byte", ErrInvalidInput, field)
	}
	if oneLine && strings.ContainsAny(value, "\r\n") {
		return "", fmt.Errorf("%w: %s must be one line", ErrInvalidInput, field)
	}
	if looksLikeSecret(value) {
		return "", fmt.Errorf("%w: %s", ErrPotentialSecret, field)
	}
	return value, nil
}

func looksLikeSecret(value string) bool {
	lower := strings.ToLower(value)
	markers := []string{
		"-----begin ",
		"private key-----",
		"authorization: bearer ",
		"authorization=bearer ",
		"password=",
		"password:",
		"passwd=",
		"api_key=",
		"api_key:",
		"api-key=",
		"secret=",
		"secret:",
		"access_token=",
		"access_token:",
		"token=",
		"token:",
	}
	for _, marker := range markers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func validateID(id string) error {
	if len(id) < 1 || len(id) > 63 || id[0] == '-' || id[len(id)-1] == '-' {
		return ErrInvalidID
	}
	for _, runeValue := range id {
		if (runeValue >= 'a' && runeValue <= 'z') || (runeValue >= '0' && runeValue <= '9') || runeValue == '-' {
			continue
		}
		return ErrInvalidID
	}
	return nil
}

func slugify(name string) string {
	var builder strings.Builder
	lastHyphen := false
	for _, runeValue := range strings.ToLower(name) {
		if (runeValue >= 'a' && runeValue <= 'z') || (runeValue >= '0' && runeValue <= '9') {
			builder.WriteRune(runeValue)
			lastHyphen = false
			continue
		}
		if builder.Len() > 0 && !lastHyphen {
			builder.WriteByte('-')
			lastHyphen = true
		}
	}
	id := strings.Trim(builder.String(), "-")
	if id == "" {
		id = "skill"
	}
	if len(id) > 63 {
		id = strings.TrimRight(id[:63], "-")
	}
	return id
}

func proposalMarkdown(input DraftInput) string {
	return "# Skill proposal: " + input.Name + "\n\n" +
		"## Description\n\n" + input.Description + "\n\n" +
		"## Source task\n\n" + input.SourceTask + "\n\n" +
		"## Required review\n\n" +
		"- Confirm the workflow has no credential, payment, identity, or irreversible-action shortcuts.\n" +
		"- Define approval boundaries and a safe validation environment before enabling it.\n"
}

func skillMarkdown(input DraftInput) string {
	return "---\n" +
		"name: \"" + yamlString(input.Name) + "\"\n" +
		"description: \"" + yamlString(input.Description) + "\"\n" +
		"---\n\n" +
		"# " + input.Name + "\n\n" +
		"## When to use\n\n" +
		"Use this skill when the requested outcome matches: " + input.Description + "\n\n" +
		"## Preconditions\n\n" +
		"- Confirm the required workspace, account access, and non-secret inputs are available.\n" +
		"- Ask for approval before any external, destructive, financial, identity, or credential-related action.\n\n" +
		"## Steps\n\n" +
		"1. Restate the intended safe outcome.\n" +
		"2. Follow the source task: " + input.SourceTask + "\n" +
		"3. Stop for approval at each stated safety boundary.\n\n" +
		"## Safety boundaries\n\n" +
		"- Never request, record, or replay passwords, passkeys, one-time codes, API keys, payment data, or CAPTCHA solutions.\n" +
		"- Do not perform writes, sends, purchases, or account changes without explicit approval.\n" +
		"- Pause and hand control to a human for authentication, identity verification, or unexpected sensitive data.\n\n" +
		"## Verification\n\n" +
		"- Verify the expected outcome with safe test data.\n" +
		"- Report what changed, what was verified, and any remaining approval or follow-up needed.\n"
}

func yamlString(value string) string {
	replacer := strings.NewReplacer("\\", "\\\\", "\"", "\\\"", "\n", "\\n", "\r", "\\r")
	return replacer.Replace(value)
}

func contentHash(draft Draft, proposal, skill string) string {
	parts := []string{
		"openagentfleet-skill-workshop-v1",
		draft.ID,
		draft.Name,
		draft.Description,
		draft.SourceTask,
		proposal,
		skill,
	}
	hash := sha256.New()
	for _, part := range parts {
		_, _ = io.WriteString(hash, strconv.Itoa(len(part)))
		_, _ = io.WriteString(hash, ":")
		_, _ = io.WriteString(hash, part)
		_, _ = io.WriteString(hash, "\n")
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func hashText(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func reviewPath(dir string, revision int) string {
	return filepath.Join(dir, "reviews", revisionName(revision)+".json")
}

func safeTestPath(dir string, revision int) string {
	return filepath.Join(dir, "tests", revisionName(revision)+".json")
}

func revisionName(revision int) string {
	return fmt.Sprintf("r%06d", revision)
}

func versionName(version int) string {
	return fmt.Sprintf("v%06d", version)
}

func nextVersion(versionsDir string) (int, error) {
	entries, err := os.ReadDir(versionsDir)
	if err != nil {
		return 0, err
	}
	maxVersion := 0
	for _, entry := range entries {
		if !entry.IsDir() || len(entry.Name()) != 7 || entry.Name()[0] != 'v' {
			continue
		}
		version, err := strconv.Atoi(entry.Name()[1:])
		if err != nil || version < 1 {
			continue
		}
		if version > maxVersion {
			maxVersion = version
		}
	}
	return maxVersion + 1, nil
}

func readEnabledVersion(dir string) (enabledVersion, error) {
	if err := assertDir(dir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return enabledVersion{}, ErrNotFound
		}
		return enabledVersion{}, err
	}
	manifest, err := readRequiredJSON[enabledVersion](filepath.Join(dir, "manifest.json"))
	if err != nil {
		return enabledVersion{}, err
	}
	if manifest.DraftID == "" || manifest.Version < 1 || len(manifest.SkillSHA256) != sha256.Size*2 || len(manifest.ContentSHA256) != sha256.Size*2 {
		return enabledVersion{}, ErrContentChanged
	}
	skill, err := readRegularFile(filepath.Join(dir, "SKILL.md"), maxFileBytes)
	if err != nil {
		return enabledVersion{}, err
	}
	if hashText(string(skill)) != manifest.SkillSHA256 {
		return enabledVersion{}, ErrContentChanged
	}
	return manifest, nil
}

func validState(state State) bool {
	switch state {
	case StateDraft, StateReviewed, StateTested, StateEnabled:
		return true
	default:
		return false
	}
}

func transitionError(state State, operation string) error {
	return fmt.Errorf("%w: cannot %s while state is %q", ErrInvalidTransition, operation, state)
}
