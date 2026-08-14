package skillworkshop

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLifecycleGeneratesPortableSkillAndRequiresApprovedSafePath(t *testing.T) {
	workshop := newWorkshop(t)
	draft, err := workshop.Create(DraftInput{
		Name:        "GitHub release notes",
		Description: "Prepare a reviewed release-note summary.",
		SourceTask:  "Inspect merged pull requests and prepare a draft without publishing it.",
	})
	if err != nil {
		t.Fatal(err)
	}
	if draft.ID != "github-release-notes" || draft.State != StateDraft || draft.Revision != 1 || len(draft.ContentSHA256) != 64 {
		t.Fatalf("new draft = %#v", draft)
	}

	inspection, err := workshop.Inspect(draft.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, heading := range []string{"## When to use", "## Preconditions", "## Steps", "## Safety boundaries", "## Verification"} {
		if !strings.Contains(inspection.Skill, heading) {
			t.Fatalf("generated SKILL.md is missing %q:\n%s", heading, inspection.Skill)
		}
	}
	if !strings.Contains(inspection.Proposal, "## Source task") {
		t.Fatalf("proposal markdown = %q", inspection.Proposal)
	}

	if _, err := workshop.MarkSafeTest(draft.ID, SafeTestInput{Runner: "local", Passed: true}); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("safe test before review error = %v, want invalid transition", err)
	}
	if _, err := workshop.Enable(draft.ID, t.TempDir()); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("enable before review/test error = %v, want invalid transition", err)
	}

	rejected, err := workshop.RecordSecurityReview(draft.ID, SecurityReviewInput{
		Reviewer: "security reviewer",
		Approved: false,
		Findings: []string{"Add an approval boundary before publishing."},
	})
	if err != nil {
		t.Fatal(err)
	}
	if rejected.State != StateDraft {
		t.Fatalf("rejected review state = %q, want draft", rejected.State)
	}
	rejectedInspection, err := workshop.Inspect(draft.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rejectedInspection.Review == nil || rejectedInspection.Review.Approved {
		t.Fatalf("rejected review = %#v", rejectedInspection.Review)
	}

	reviewed, err := workshop.RecordSecurityReview(draft.ID, SecurityReviewInput{
		Reviewer: "security reviewer",
		Approved: true,
		Notes:    "No secrets, payment, or irreversible action is included.",
	})
	if err != nil {
		t.Fatal(err)
	}
	if reviewed.State != StateReviewed {
		t.Fatalf("reviewed state = %q, want reviewed", reviewed.State)
	}

	failed, err := workshop.MarkSafeTest(draft.ID, SafeTestInput{
		Runner:   "safe local fixture",
		Passed:   false,
		Evidence: "Fixture showed a missing approval prompt.",
	})
	if err != nil {
		t.Fatal(err)
	}
	if failed.State != StateReviewed {
		t.Fatalf("failed test state = %q, want reviewed", failed.State)
	}

	tested, err := workshop.MarkSafeTest(draft.ID, SafeTestInput{
		Runner:   "safe local fixture",
		Passed:   true,
		Evidence: "The fixture produced a draft only and asked before publishing.",
	})
	if err != nil {
		t.Fatal(err)
	}
	if tested.State != StateTested {
		t.Fatalf("tested state = %q, want tested", tested.State)
	}

	enabledRoot := t.TempDir()
	deployment, err := workshop.Enable(draft.ID, enabledRoot)
	if err != nil {
		t.Fatal(err)
	}
	if !deployment.Active || deployment.Version != 1 || deployment.ContentSHA256 != draft.ContentSHA256 {
		t.Fatalf("deployment = %#v", deployment)
	}
	enabledPath := filepath.Join(enabledRoot, draft.ID, "versions", "v000001", "SKILL.md")
	enabledSkill, err := os.ReadFile(enabledPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(enabledSkill) != inspection.Skill {
		t.Fatal("enabled SKILL.md does not match the reviewed, tested draft")
	}
	info, err := os.Stat(enabledPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o222 != 0 {
		t.Fatalf("enabled skill must be read-only, mode = %#o", info.Mode().Perm())
	}
	if _, err := workshop.Enable(draft.ID, enabledRoot); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("second enable error = %v, want invalid transition", err)
	}

	final, err := workshop.Inspect(draft.ID)
	if err != nil {
		t.Fatal(err)
	}
	if final.Draft.State != StateEnabled || final.Review == nil || !final.Review.Approved || final.SafeTest == nil || !final.SafeTest.Passed {
		t.Fatalf("final inspection = %#v", final)
	}
}

func TestRevisionVersionsAreRetainedAcrossDisableAndRollback(t *testing.T) {
	workshop := newWorkshop(t)
	draft, err := workshop.Create(DraftInput{
		ID:          "release-workflow",
		Name:        "Release workflow",
		Description: "Prepare a release summary.",
		SourceTask:  "Read changelog entries and create a draft.",
	})
	if err != nil {
		t.Fatal(err)
	}
	enabledRoot := t.TempDir()
	first := promote(t, workshop, draft.ID, enabledRoot)
	if first.Version != 1 {
		t.Fatalf("first deployment = %#v", first)
	}
	firstSkillPath := filepath.Join(enabledRoot, draft.ID, "versions", "v000001", "SKILL.md")
	firstSkill, err := os.ReadFile(firstSkillPath)
	if err != nil {
		t.Fatal(err)
	}

	revised, err := workshop.Revise(draft.ID, DraftInput{
		Name:        "Release workflow",
		Description: "Prepare a release summary with explicit verification.",
		SourceTask:  "Read changelog entries, create a draft, and verify the source links.",
	})
	if err != nil {
		t.Fatal(err)
	}
	if revised.Revision != 2 || revised.State != StateDraft || revised.ContentSHA256 == draft.ContentSHA256 {
		t.Fatalf("revised draft = %#v", revised)
	}
	revisedInspection, err := workshop.Inspect(draft.ID)
	if err != nil {
		t.Fatal(err)
	}
	if revisedInspection.Review != nil || revisedInspection.SafeTest != nil {
		t.Fatalf("new revision must not inherit review/test: %#v", revisedInspection)
	}

	second := promote(t, workshop, draft.ID, enabledRoot)
	if second.Version != 2 || !second.Active {
		t.Fatalf("second deployment = %#v", second)
	}
	secondSkillPath := filepath.Join(enabledRoot, draft.ID, "versions", "v000002", "SKILL.md")
	secondSkill, err := os.ReadFile(secondSkillPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(firstSkill) == string(secondSkill) {
		t.Fatal("revised immutable version unexpectedly has the old content")
	}

	disabled, err := workshop.Disable(draft.ID, enabledRoot)
	if err != nil {
		t.Fatal(err)
	}
	if disabled.Active || disabled.Version != 2 {
		t.Fatalf("disabled deployment = %#v", disabled)
	}
	if got, err := os.ReadFile(firstSkillPath); err != nil || string(got) != string(firstSkill) {
		t.Fatalf("v1 changed or disappeared after disable: %v / %q", err, got)
	}
	if got, err := os.ReadFile(secondSkillPath); err != nil || string(got) != string(secondSkill) {
		t.Fatalf("v2 changed or disappeared after disable: %v / %q", err, got)
	}

	rolledBack, err := workshop.Rollback(draft.ID, enabledRoot, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !rolledBack.Active || rolledBack.Version != 1 || rolledBack.ContentSHA256 != first.ContentSHA256 {
		t.Fatalf("rollback = %#v", rolledBack)
	}
	current, err := workshop.Deployment(draft.ID, enabledRoot)
	if err != nil {
		t.Fatal(err)
	}
	if current != rolledBack {
		t.Fatalf("current deployment = %#v, want %#v", current, rolledBack)
	}
	if got, err := os.ReadFile(secondSkillPath); err != nil || string(got) != string(secondSkill) {
		t.Fatalf("v2 changed or disappeared after rollback: %v / %q", err, got)
	}
}

func TestRejectsTraversalSecretsAndTamperedContent(t *testing.T) {
	root := t.TempDir()
	workshop, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := workshop.Create(DraftInput{
		ID:          "../outside",
		Name:        "Unsafe",
		Description: "Description.",
		SourceTask:  "Task.",
	}); !errors.Is(err, ErrInvalidID) {
		t.Fatalf("traversal error = %v, want invalid ID", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(root), "outside")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("traversal unexpectedly created an outside directory: %v", err)
	}
	if _, err := workshop.Create(DraftInput{
		ID:          "has-secret",
		Name:        "Unsafe secret",
		Description: "Description.",
		SourceTask:  "Use api_key=not-allowed.",
	}); !errors.Is(err, ErrPotentialSecret) {
		t.Fatalf("secret input error = %v, want potential secret", err)
	}

	draft, err := workshop.Create(DraftInput{
		ID:          "tamper-check",
		Name:        "Tamper check",
		Description: "Check content integrity.",
		SourceTask:  "Prepare a safe draft.",
	})
	if err != nil {
		t.Fatal(err)
	}
	skillPath := filepath.Join(root, draft.ID, "revisions", "r000001", "SKILL.md")
	if err := os.WriteFile(skillPath, []byte("# altered"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := workshop.Inspect(draft.ID); !errors.Is(err, ErrContentChanged) {
		t.Fatalf("tampered inspection error = %v, want content changed", err)
	}
	if _, err := workshop.RecordSecurityReview(draft.ID, SecurityReviewInput{Reviewer: "reviewer", Approved: true}); !errors.Is(err, ErrContentChanged) {
		t.Fatalf("tampered review error = %v, want content changed", err)
	}
}

func TestListIsStableAndOnlyReturnsVerifiedDrafts(t *testing.T) {
	workshop := newWorkshop(t)
	for _, input := range []DraftInput{
		{ID: "zeta", Name: "Zeta", Description: "Zeta description.", SourceTask: "Draft zeta."},
		{ID: "alpha", Name: "Alpha", Description: "Alpha description.", SourceTask: "Draft alpha."},
	} {
		if _, err := workshop.Create(input); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(workshop.Root(), "README.txt"), []byte("not a draft"), 0o600); err != nil {
		t.Fatal(err)
	}
	items, err := workshop.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].ID != "alpha" || items[1].ID != "zeta" {
		t.Fatalf("list = %#v", items)
	}
}

func newWorkshop(t *testing.T) *Workshop {
	t.Helper()
	workshop, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return workshop
}

func promote(t *testing.T, workshop *Workshop, id, enabledRoot string) Deployment {
	t.Helper()
	if _, err := workshop.RecordSecurityReview(id, SecurityReviewInput{
		Reviewer: "security reviewer",
		Approved: true,
		Notes:    "Reviewed with safe local data.",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := workshop.MarkSafeTest(id, SafeTestInput{
		Runner:   "safe fixture",
		Passed:   true,
		Evidence: "Completed with no external write.",
	}); err != nil {
		t.Fatal(err)
	}
	deployment, err := workshop.Enable(id, enabledRoot)
	if err != nil {
		t.Fatal(err)
	}
	return deployment
}
