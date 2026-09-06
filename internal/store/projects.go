package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/robbyczgw-cla/openagentfleet/internal/domain"
	"github.com/robbyczgw-cla/openagentfleet/internal/id"
)

var (
	ErrProjectNotFound            = errors.New("project not found")
	ErrProjectArchived            = errors.New("project is archived")
	ErrProjectVersionConflict     = errors.New("project version conflict")
	ErrProjectAgentNotFound       = errors.New("project agent not found")
	ErrProjectAgentNotMember      = errors.New("agent is not a project member")
	ErrProjectSubjectNotFound     = errors.New("project association subject not found")
	ErrProjectAssociationNotFound = errors.New("project association not found")
	ErrProjectSnapshotNotFound    = errors.New("project task snapshot not found")
	ErrProjectRunAgentMismatch    = errors.New("run does not belong to project agent")
)

// IsProjectAccessBlocked identifies current-state authorization failures that
// must stop a queued run before provider execution.
func IsProjectAccessBlocked(err error) bool {
	return errors.Is(err, ErrProjectAgentNotMember) || errors.Is(err, ErrProjectArchived)
}

const projectColumns = `p.id,p.name,r.brief,p.status,p.version,p.current_brief_revision,p.created_at,p.updated_at,p.archived_at`

// MigrateProjects creates only additive project tables. Store.Open should call
// this during bootstrap; each method also calls it so staged integrations work.
func (s *Store) MigrateProjects(ctx context.Context) error {
	const schema = `
CREATE TABLE IF NOT EXISTS projects (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  status TEXT NOT NULL CHECK(status IN ('active','archived')),
  version INTEGER NOT NULL CHECK(version > 0),
  current_brief_revision INTEGER NOT NULL CHECK(current_brief_revision > 0),
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  archived_at TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS project_brief_revisions (
  project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  revision INTEGER NOT NULL CHECK(revision > 0),
  brief TEXT NOT NULL,
  created_at TEXT NOT NULL,
  PRIMARY KEY(project_id,revision)
);
CREATE TABLE IF NOT EXISTS project_members (
  project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  bot_id TEXT NOT NULL REFERENCES bots(id),
  created_at TEXT NOT NULL,
  PRIMARY KEY(project_id,bot_id)
);
CREATE INDEX IF NOT EXISTS project_members_bot_idx ON project_members(bot_id,project_id);
CREATE TABLE IF NOT EXISTS project_associations (
  project_id TEXT NOT NULL REFERENCES projects(id),
  subject_type TEXT NOT NULL CHECK(subject_type IN ('conversation','routine')),
  subject_id TEXT NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  PRIMARY KEY(subject_type,subject_id)
);
CREATE INDEX IF NOT EXISTS project_associations_project_idx ON project_associations(project_id,subject_type,subject_id);
CREATE TABLE IF NOT EXISTS project_task_snapshots (
  run_id TEXT PRIMARY KEY REFERENCES runs(id) ON DELETE CASCADE,
  project_id TEXT NOT NULL REFERENCES projects(id),
  project_name TEXT NOT NULL,
  brief_revision INTEGER NOT NULL,
  brief TEXT NOT NULL,
  created_at TEXT NOT NULL,
  FOREIGN KEY(project_id,brief_revision) REFERENCES project_brief_revisions(project_id,revision)
);
CREATE INDEX IF NOT EXISTS project_task_snapshots_project_idx ON project_task_snapshots(project_id,brief_revision);
CREATE TABLE IF NOT EXISTS project_run_subjects (
 run_id TEXT PRIMARY KEY REFERENCES runs(id) ON DELETE CASCADE,
 subject_type TEXT NOT NULL, subject_id TEXT NOT NULL
);
CREATE TRIGGER IF NOT EXISTS project_brief_revisions_no_update
BEFORE UPDATE ON project_brief_revisions BEGIN
  SELECT RAISE(ABORT,'project brief revisions are append-only');
END;
CREATE TRIGGER IF NOT EXISTS project_brief_revisions_no_delete
BEFORE DELETE ON project_brief_revisions BEGIN
  SELECT RAISE(ABORT,'project brief revisions are append-only');
END;
CREATE TRIGGER IF NOT EXISTS project_task_snapshots_no_update
BEFORE UPDATE ON project_task_snapshots BEGIN
  SELECT RAISE(ABORT,'project task snapshots are immutable');
END;
`
	if _, err := s.db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("migrate project schema: %w", err)
	}
	return nil
}

func (s *Store) CreateProject(ctx context.Context, draft domain.ProjectDraft) (domain.Project, error) {
	if err := s.MigrateProjects(ctx); err != nil {
		return domain.Project{}, err
	}
	draft, err := domain.NormalizeProjectDraft(draft)
	if err != nil {
		return domain.Project{}, err
	}
	timestamp := now()
	project := domain.Project{
		ID: id.New("proj"), Name: draft.Name, Brief: draft.Brief, Status: domain.ProjectStatusActive,
		Version: 1, BriefRevision: 1, CreatedAt: timestamp, UpdatedAt: timestamp,
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Project{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `INSERT INTO projects
(id,name,status,version,current_brief_revision,created_at,updated_at,archived_at) VALUES(?,?,?,?,?,?,?,'')`,
		project.ID, project.Name, project.Status, project.Version, project.BriefRevision, timestamp, timestamp); err != nil {
		return domain.Project{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO project_brief_revisions(project_id,revision,brief,created_at) VALUES(?,?,?,?)`,
		project.ID, project.BriefRevision, project.Brief, timestamp); err != nil {
		return domain.Project{}, err
	}
	members, err := replaceProjectMembers(ctx, tx, project.ID, draft.AgentIDs, timestamp)
	if err != nil {
		return domain.Project{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Project{}, err
	}
	project.Members = members
	return project, nil
}

func (s *Store) ListProjects(ctx context.Context, includeArchived bool) ([]domain.Project, error) {
	if err := s.MigrateProjects(ctx); err != nil {
		return nil, err
	}
	query := `SELECT ` + projectColumns + ` FROM projects p JOIN project_brief_revisions r
ON r.project_id=p.id AND r.revision=p.current_brief_revision`
	if !includeArchived {
		query += ` WHERE p.status='active'`
	}
	query += ` ORDER BY p.updated_at DESC,p.id DESC`
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	projects := make([]domain.Project, 0)
	for rows.Next() {
		item, err := scanProject(rows)
		if err != nil {
			return nil, err
		}
		projects = append(projects, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range projects {
		projects[i].Members, err = s.listProjectMembers(ctx, projects[i].ID)
		if err != nil {
			return nil, err
		}
	}
	return projects, nil
}

func (s *Store) GetProject(ctx context.Context, projectID string) (domain.Project, error) {
	if err := s.MigrateProjects(ctx); err != nil {
		return domain.Project{}, err
	}
	item, err := loadProject(ctx, s.db, projectID)
	if err != nil {
		return domain.Project{}, err
	}
	item.Members, err = s.listProjectMembers(ctx, item.ID)
	return item, err
}

func (s *Store) ListProjectBriefRevisions(ctx context.Context, projectID string) ([]domain.ProjectBriefRevision, error) {
	if err := s.MigrateProjects(ctx); err != nil {
		return nil, err
	}
	if _, err := loadProject(ctx, s.db, projectID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT project_id,revision,brief,created_at FROM project_brief_revisions WHERE project_id=? ORDER BY revision DESC`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]domain.ProjectBriefRevision, 0)
	for rows.Next() {
		var item domain.ProjectBriefRevision
		if err := rows.Scan(&item.ProjectID, &item.Revision, &item.Brief, &item.CreatedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) UpdateProject(ctx context.Context, projectID string, update domain.ProjectUpdate) (domain.Project, error) {
	if err := s.MigrateProjects(ctx); err != nil {
		return domain.Project{}, err
	}
	draft, err := domain.NormalizeProjectDraft(domain.ProjectDraft{Name: update.Name, Brief: update.Brief, AgentIDs: update.AgentIDs})
	if err != nil {
		return domain.Project{}, err
	}
	if update.ExpectedVersion < 1 {
		return domain.Project{}, errors.New("expected_version must be positive")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Project{}, err
	}
	defer func() { _ = tx.Rollback() }()
	project, err := loadProject(ctx, tx, projectID)
	if err != nil {
		return domain.Project{}, err
	}
	if project.Status == domain.ProjectStatusArchived {
		return domain.Project{}, ErrProjectArchived
	}
	if project.Version != update.ExpectedVersion {
		return domain.Project{}, ErrProjectVersionConflict
	}
	timestamp := now()
	briefRevision := project.BriefRevision
	if draft.Brief != project.Brief {
		briefRevision++
		if _, err := tx.ExecContext(ctx, `INSERT INTO project_brief_revisions(project_id,revision,brief,created_at) VALUES(?,?,?,?)`,
			project.ID, briefRevision, draft.Brief, timestamp); err != nil {
			return domain.Project{}, err
		}
	}
	result, err := tx.ExecContext(ctx, `UPDATE projects SET name=?,version=version+1,current_brief_revision=?,updated_at=?
WHERE id=? AND version=? AND status='active'`, draft.Name, briefRevision, timestamp, project.ID, update.ExpectedVersion)
	if err != nil {
		return domain.Project{}, err
	}
	if changed, err := result.RowsAffected(); err != nil {
		return domain.Project{}, err
	} else if changed != 1 {
		return domain.Project{}, ErrProjectVersionConflict
	}
	members, err := replaceProjectMembers(ctx, tx, project.ID, draft.AgentIDs, timestamp)
	if err != nil {
		return domain.Project{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Project{}, err
	}
	project.Name = draft.Name
	project.Brief = draft.Brief
	project.Version++
	project.BriefRevision = briefRevision
	project.UpdatedAt = timestamp
	project.Members = members
	return project, nil
}

func (s *Store) ArchiveProject(ctx context.Context, projectID string, expectedVersion int64) (domain.Project, error) {
	if err := s.MigrateProjects(ctx); err != nil {
		return domain.Project{}, err
	}
	if expectedVersion < 1 {
		return domain.Project{}, errors.New("expected_version must be positive")
	}
	timestamp := now()
	result, err := s.db.ExecContext(ctx, `UPDATE projects SET status='archived',version=version+1,updated_at=?,archived_at=?
WHERE id=? AND version=? AND status='active'`, timestamp, timestamp, projectID, expectedVersion)
	if err != nil {
		return domain.Project{}, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return domain.Project{}, err
	}
	if changed == 0 {
		project, loadErr := s.GetProject(ctx, projectID)
		if loadErr != nil {
			return domain.Project{}, loadErr
		}
		if project.Status == domain.ProjectStatusArchived {
			return domain.Project{}, ErrProjectArchived
		}
		return domain.Project{}, ErrProjectVersionConflict
	}
	return s.GetProject(ctx, projectID)
}

func (s *Store) SetProjectAssociation(ctx context.Context, projectID string, subjectType domain.ProjectSubjectType, subjectID string) (domain.ProjectAssociation, error) {
	if err := s.MigrateProjects(ctx); err != nil {
		return domain.ProjectAssociation{}, err
	}
	if !domain.ValidProjectSubjectType(subjectType) || strings.TrimSpace(subjectID) == "" {
		return domain.ProjectAssociation{}, errors.New("valid project association subject is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.ProjectAssociation{}, err
	}
	defer func() { _ = tx.Rollback() }()
	project, err := loadProject(ctx, tx, projectID)
	if err != nil {
		return domain.ProjectAssociation{}, err
	}
	if project.Status == domain.ProjectStatusArchived {
		return domain.ProjectAssociation{}, ErrProjectArchived
	}
	agentID, err := projectSubjectAgentID(ctx, tx, subjectType, subjectID)
	if err != nil {
		return domain.ProjectAssociation{}, err
	}
	if err := requireProjectMember(ctx, tx, projectID, agentID); err != nil {
		return domain.ProjectAssociation{}, err
	}
	timestamp := now()
	_, err = tx.ExecContext(ctx, `INSERT INTO project_associations(project_id,subject_type,subject_id,created_at,updated_at)
VALUES(?,?,?,?,?) ON CONFLICT(subject_type,subject_id) DO UPDATE SET project_id=excluded.project_id,updated_at=excluded.updated_at`,
		projectID, subjectType, subjectID, timestamp, timestamp)
	if err != nil {
		return domain.ProjectAssociation{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.ProjectAssociation{}, err
	}
	return s.GetProjectAssociation(ctx, subjectType, subjectID)
}

func (s *Store) GetProjectAssociation(ctx context.Context, subjectType domain.ProjectSubjectType, subjectID string) (domain.ProjectAssociation, error) {
	if err := s.MigrateProjects(ctx); err != nil {
		return domain.ProjectAssociation{}, err
	}
	var item domain.ProjectAssociation
	err := s.db.QueryRowContext(ctx, `SELECT a.project_id,p.name,a.subject_type,a.subject_id,a.created_at,a.updated_at
FROM project_associations a JOIN projects p ON p.id=a.project_id WHERE a.subject_type=? AND a.subject_id=?`,
		subjectType, subjectID).Scan(&item.ProjectID, &item.ProjectName, &item.SubjectType, &item.SubjectID, &item.CreatedAt, &item.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ProjectAssociation{}, ErrProjectAssociationNotFound
	}
	return item, err
}

func (s *Store) DeleteProjectAssociation(ctx context.Context, subjectType domain.ProjectSubjectType, subjectID string) error {
	if err := s.MigrateProjects(ctx); err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM project_associations WHERE subject_type=? AND subject_id=?`, subjectType, subjectID)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed == 0 {
		return ErrProjectAssociationNotFound
	}
	return nil
}

// SnapshotProjectForRun resolves only an explicit subject association. It
// checks the run owner and current membership before copying the current brief.
// An existing snapshot keeps its revision, but current status and membership
// still gate every enqueue retry and later provider preflight.
func (s *Store) SnapshotProjectForRun(ctx context.Context, runID string, subjectType domain.ProjectSubjectType, subjectID, agentID string) (*domain.ProjectTaskSnapshot, error) {
	if !domain.ValidProjectSubjectType(subjectType) {
		return nil, errors.New("invalid project association subject")
	}
	if err := validateProjectRunSubject(ctx, s.db, runID, subjectType, subjectID, agentID); err != nil {
		return nil, err
	}
	// Subject provenance belongs to the run even when no project is associated
	// yet. The standalone write can wait on SQLite's busy timeout without the
	// read-to-write upgrade hazard of a deferred transaction.
	if _, err := s.db.ExecContext(ctx, `INSERT INTO project_run_subjects(run_id,subject_type,subject_id) VALUES(?,?,?) ON CONFLICT(run_id) DO NOTHING`, runID, subjectType, subjectID); err != nil {
		return nil, err
	}
	if existing, err := getTaskProjectSnapshot(ctx, s.db, runID); err == nil {
		if err := authorizeProjectSnapshot(ctx, s.db, existing.ProjectID, agentID); err != nil {
			return nil, err
		}
		return &existing, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	var projectID string
	err := s.db.QueryRowContext(ctx, `SELECT project_id FROM project_associations WHERE subject_type=? AND subject_id=?`, subjectType, subjectID).Scan(&projectID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	conn, rollback, err := s.beginProjectImmediate(ctx)
	if err != nil {
		return nil, err
	}
	defer rollback()
	if err := validateProjectRunSubject(ctx, conn, runID, subjectType, subjectID, agentID); err != nil {
		return nil, err
	}
	if existing, err := getTaskProjectSnapshot(ctx, conn, runID); err == nil {
		if err := authorizeProjectSnapshot(ctx, conn, existing.ProjectID, agentID); err != nil {
			return nil, err
		}
		if err := commitProjectImmediate(ctx, conn); err != nil {
			return nil, err
		}
		return &existing, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	err = conn.QueryRowContext(ctx, `SELECT project_id FROM project_associations WHERE subject_type=? AND subject_id=?`, subjectType, subjectID).Scan(&projectID)
	if errors.Is(err, sql.ErrNoRows) {
		if err := commitProjectImmediate(ctx, conn); err != nil {
			return nil, err
		}
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	project, err := loadProject(ctx, conn, projectID)
	if err != nil {
		return nil, err
	}
	if project.Status == domain.ProjectStatusArchived {
		return nil, ErrProjectArchived
	}
	if err := requireProjectMember(ctx, conn, projectID, agentID); err != nil {
		return nil, err
	}
	snapshot := domain.ProjectTaskSnapshot{
		RunID: runID, ProjectID: project.ID, ProjectName: project.Name,
		BriefRevision: project.BriefRevision, Brief: project.Brief, CreatedAt: now(),
	}
	if _, err := conn.ExecContext(ctx, `INSERT INTO project_task_snapshots(run_id,project_id,project_name,brief_revision,brief,created_at) VALUES(?,?,?,?,?,?)`,
		snapshot.RunID, snapshot.ProjectID, snapshot.ProjectName, snapshot.BriefRevision, snapshot.Brief, snapshot.CreatedAt); err != nil {
		return nil, err
	}
	if err := commitProjectImmediate(ctx, conn); err != nil {
		return nil, err
	}
	return &snapshot, nil
}

func (s *Store) GetTaskProjectSnapshot(ctx context.Context, runID string) (domain.ProjectTaskSnapshot, error) {
	item, err := getTaskProjectSnapshot(ctx, s.db, runID)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ProjectTaskSnapshot{}, ErrProjectSnapshotNotFound
	}
	return item, err
}

func (s *Store) SnapshotTaskFollowupProject(ctx context.Context, parentID, runID, agentID string) error {
	var subjectType domain.ProjectSubjectType
	var subjectID string
	err := s.db.QueryRowContext(ctx, `SELECT subject_type,subject_id FROM project_run_subjects WHERE run_id=?`, parentID).Scan(&subjectType, &subjectID)
	if errors.Is(err, sql.ErrNoRows) {
		subjectType = domain.ProjectSubjectConversation
		err = s.db.QueryRowContext(ctx, `SELECT conversation_id FROM runs WHERE id=?`, parentID).Scan(&subjectID)
	}
	if err != nil {
		return err
	}
	_, err = s.SnapshotProjectForRun(ctx, runID, subjectType, subjectID, agentID)
	return err
}

// ProjectBriefPromptForRun returns only the immutable project snapshot. Agent
// memories remain on their existing private prompt path.
func (s *Store) ProjectBriefPromptForRun(ctx context.Context, runID, agentID string) (string, error) {
	var runAgentID string
	if err := s.db.QueryRowContext(ctx, `SELECT bot_id FROM runs WHERE id=?`, runID).Scan(&runAgentID); errors.Is(err, sql.ErrNoRows) {
		return "", ErrProjectSubjectNotFound
	} else if err != nil {
		return "", err
	}
	if runAgentID != agentID {
		return "", ErrProjectRunAgentMismatch
	}
	snapshot, err := getTaskProjectSnapshot(ctx, s.db, runID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if err := authorizeProjectSnapshot(ctx, s.db, snapshot.ProjectID, agentID); err != nil {
		return "", err
	}
	return fmt.Sprintf("# Project brief snapshot\nProject: %s\nBrief revision: %d\n\n%s", snapshot.ProjectName, snapshot.BriefRevision, snapshot.Brief), nil
}

type projectQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (s *Store) beginProjectImmediate(ctx context.Context) (*sql.Conn, func(), error) {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return nil, func() {}, fmt.Errorf("acquire project connection: %w", err)
	}
	if _, err := conn.ExecContext(ctx, "PRAGMA busy_timeout = 5000"); err != nil {
		_ = conn.Close()
		return nil, func() {}, fmt.Errorf("configure project transaction: %w", err)
	}
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		_ = conn.Close()
		return nil, func() {}, fmt.Errorf("begin project transaction: %w", err)
	}
	rollback := func() {
		_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		_ = conn.Close()
	}
	return conn, rollback, nil
}

func commitProjectImmediate(ctx context.Context, conn *sql.Conn) error {
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return fmt.Errorf("snapshot project for run: commit: %w", err)
	}
	return nil
}

func validateProjectRunSubject(ctx context.Context, queryer projectQueryer, runID string, subjectType domain.ProjectSubjectType, subjectID, agentID string) error {
	var runAgentID, conversationID string
	if err := queryer.QueryRowContext(ctx, `SELECT bot_id,conversation_id FROM runs WHERE id=?`, runID).Scan(&runAgentID, &conversationID); errors.Is(err, sql.ErrNoRows) {
		return ErrProjectSubjectNotFound
	} else if err != nil {
		return err
	}
	if runAgentID != agentID || (subjectType == domain.ProjectSubjectConversation && conversationID != subjectID) {
		return ErrProjectRunAgentMismatch
	}
	subjectAgentID, err := projectSubjectAgentID(ctx, queryer, subjectType, subjectID)
	if err != nil {
		return err
	}
	if subjectAgentID != agentID {
		return ErrProjectRunAgentMismatch
	}
	return nil
}

func loadProject(ctx context.Context, queryer projectQueryer, projectID string) (domain.Project, error) {
	item, err := scanProject(queryer.QueryRowContext(ctx, `SELECT `+projectColumns+` FROM projects p JOIN project_brief_revisions r
ON r.project_id=p.id AND r.revision=p.current_brief_revision WHERE p.id=?`, projectID))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Project{}, ErrProjectNotFound
	}
	return item, err
}

func scanProject(row interface{ Scan(...any) error }) (domain.Project, error) {
	var item domain.Project
	err := row.Scan(&item.ID, &item.Name, &item.Brief, &item.Status, &item.Version, &item.BriefRevision,
		&item.CreatedAt, &item.UpdatedAt, &item.ArchivedAt)
	return item, err
}

func (s *Store) listProjectMembers(ctx context.Context, projectID string) ([]domain.ProjectMember, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT m.bot_id,b.name,b.title FROM project_members m JOIN bots b ON b.id=m.bot_id
WHERE m.project_id=? ORDER BY b.name,m.bot_id`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]domain.ProjectMember, 0)
	for rows.Next() {
		var item domain.ProjectMember
		if err := rows.Scan(&item.AgentID, &item.Name, &item.Title); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func replaceProjectMembers(ctx context.Context, tx *sql.Tx, projectID string, agentIDs []string, timestamp string) ([]domain.ProjectMember, error) {
	if _, err := tx.ExecContext(ctx, `DELETE FROM project_members WHERE project_id=?`, projectID); err != nil {
		return nil, err
	}
	members := make([]domain.ProjectMember, 0, len(agentIDs))
	for _, agentID := range agentIDs {
		var member domain.ProjectMember
		member.AgentID = agentID
		if err := tx.QueryRowContext(ctx, `SELECT name,title FROM bots WHERE id=?`, agentID).Scan(&member.Name, &member.Title); errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("%w: %s", ErrProjectAgentNotFound, agentID)
		} else if err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO project_members(project_id,bot_id,created_at) VALUES(?,?,?)`, projectID, agentID, timestamp); err != nil {
			return nil, err
		}
		members = append(members, member)
	}
	return members, nil
}

func requireProjectMember(ctx context.Context, queryer projectQueryer, projectID, agentID string) error {
	var exists int
	err := queryer.QueryRowContext(ctx, `SELECT 1 FROM project_members WHERE project_id=? AND bot_id=?`, projectID, agentID).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrProjectAgentNotMember
	}
	return err
}

func authorizeProjectSnapshot(ctx context.Context, queryer projectQueryer, projectID, agentID string) error {
	var status domain.ProjectStatus
	var member bool
	err := queryer.QueryRowContext(ctx, `SELECT p.status,EXISTS(
SELECT 1 FROM project_members m WHERE m.project_id=p.id AND m.bot_id=?) FROM projects p WHERE p.id=?`, agentID, projectID).Scan(&status, &member)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrProjectNotFound
	}
	if err != nil {
		return err
	}
	if status == domain.ProjectStatusArchived {
		return ErrProjectArchived
	}
	if !member {
		return ErrProjectAgentNotMember
	}
	return nil
}

func projectSubjectAgentID(ctx context.Context, queryer projectQueryer, subjectType domain.ProjectSubjectType, subjectID string) (string, error) {
	query := ""
	switch subjectType {
	case domain.ProjectSubjectConversation:
		query = `SELECT bot_id FROM conversations WHERE id=?`
	case domain.ProjectSubjectRoutine:
		query = `SELECT bot_id FROM routine_schedules WHERE id=?`
	default:
		return "", errors.New("invalid project association subject")
	}
	var agentID string
	if err := queryer.QueryRowContext(ctx, query, subjectID).Scan(&agentID); errors.Is(err, sql.ErrNoRows) {
		return "", ErrProjectSubjectNotFound
	} else if err != nil {
		return "", err
	}
	return agentID, nil
}

func getTaskProjectSnapshot(ctx context.Context, queryer projectQueryer, runID string) (domain.ProjectTaskSnapshot, error) {
	var item domain.ProjectTaskSnapshot
	err := queryer.QueryRowContext(ctx, `SELECT run_id,project_id,project_name,brief_revision,brief,created_at FROM project_task_snapshots WHERE run_id=?`, runID).
		Scan(&item.RunID, &item.ProjectID, &item.ProjectName, &item.BriefRevision, &item.Brief, &item.CreatedAt)
	return item, err
}
