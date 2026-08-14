package research

import (
	"strings"
	"testing"
	"time"
)

func now() time.Time { return time.Date(2026, 8, 12, 11, 0, 0, 0, time.UTC) }
func plan() WorkPlan {
	return WorkPlan{MaxSources: 2, MaxClaims: 3, MaxArtifacts: 2, MaxDurationSeconds: 300, AllowNetwork: true}
}
func source() Source {
	return Source{ID: "official-docs", Kind: SourcePrimary, URL: "https://example.com/docs", Title: "Official documentation", RetrievedAt: now(), ContentDigestSHA256: strings.Repeat("a", 64)}
}

func TestResearchDefaultsDenyExecution(t *testing.T) {
	r, err := New("browser-research", "compare browser controls", plan(), now())
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != StatusDraft {
		t.Fatalf("unexpected state: %s", r.Status)
	}
	if _, err := r.Queue(Policy{}, now()); err == nil {
		t.Fatal("expected experimental default deny")
	}
	r, err = r.Queue(Policy{ExperimentalResearchEnabled: true}, now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Start(Policy{ExperimentalResearchEnabled: true}, now()); err == nil {
		t.Fatal("expected network default deny")
	}
	r, err = r.Start(Policy{ExperimentalResearchEnabled: true, NetworkFetchEnabled: true}, now())
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != StatusRunning {
		t.Fatalf("unexpected state: %s", r.Status)
	}
}

func TestSourceCitationClaimArtifactBindingsAndCompletion(t *testing.T) {
	r, _ := New("browser-research", "compare browser controls", plan(), now())
	r, _ = r.Queue(Policy{ExperimentalResearchEnabled: true}, now())
	r, _ = r.Start(Policy{ExperimentalResearchEnabled: true, NetworkFetchEnabled: true}, now())
	var err error
	r, err = r.AddSource(source(), now())
	if err != nil {
		t.Fatal(err)
	}
	r, err = r.AddCitation(Citation{ID: "docs-section", SourceID: "official-docs", Locator: "section 2", Note: "documents the capability"}, now())
	if err != nil {
		t.Fatal(err)
	}
	r, err = r.AddClaim(Claim{ID: "supported-feature", Text: "The official documentation describes the capability.", Evidence: EvidenceVerified, CitationIDs: []string{"docs-section"}}, now())
	if err != nil {
		t.Fatal(err)
	}
	r, err = r.AddArtifact(Artifact{ID: "report", Kind: ArtifactReport, URI: "artifact://browser-research/report", DigestSHA256: DigestContent([]byte("report v1")), ClaimIDs: []string{"supported-feature"}, SourceIDs: []string{"official-docs"}}, now())
	if err != nil {
		t.Fatal(err)
	}
	r, err = r.Complete(now())
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != StatusCompleted {
		t.Fatalf("not completed: %s", r.Status)
	}
	if _, err := r.AddSource(source(), now()); err == nil {
		t.Fatal("completed research mutated")
	}
}

func TestInferenceMustExplainAndBeCited(t *testing.T) {
	r, _ := New("inference-run", "infer something", plan(), now())
	r, _ = r.AddSource(source(), now())
	r, _ = r.AddCitation(Citation{ID: "cite", SourceID: "official-docs"}, now())
	if _, err := r.AddClaim(Claim{ID: "inference", Text: "Likely outcome", Evidence: EvidenceInference, CitationIDs: []string{"cite"}}, now()); err == nil {
		t.Fatal("inference without basis accepted")
	}
	if _, err := r.AddClaim(Claim{ID: "unsupported", Text: "Unsupported", Evidence: EvidenceVerified}, now()); err == nil {
		t.Fatal("uncited verified claim accepted")
	}
	if _, err := r.AddClaim(Claim{ID: "inference", Text: "Likely outcome", Evidence: EvidenceInference, CitationIDs: []string{"cite"}, InferenceBasis: "Derived from the cited implementation limitation."}, now()); err != nil {
		t.Fatal(err)
	}
}

func TestBudgetAndUnsafeURLsAreRejected(t *testing.T) {
	if _, err := New("id", "q", WorkPlan{}, now()); err == nil {
		t.Fatal("invalid plan accepted")
	}
	r, _ := New("budget-run", "bounded", WorkPlan{MaxSources: 1, MaxClaims: 1, MaxArtifacts: 1, MaxDurationSeconds: 30}, now())
	unsafe := source()
	unsafe.URL = "http://localhost:3000/private"
	if _, err := r.AddSource(unsafe, now()); err == nil {
		t.Fatal("unsafe URL accepted")
	}
	r, _ = r.AddSource(source(), now())
	if _, err := r.AddSource(Source{ID: "another", Kind: SourcePrimary, URL: "https://example.org", Title: "Other", RetrievedAt: now()}, now()); err == nil {
		t.Fatal("source budget exceeded")
	}
}

func TestCancelLifecycleAndArtifactBinding(t *testing.T) {
	r, _ := New("cancel-run", "cancel safely", plan(), now())
	r, _ = r.Queue(Policy{ExperimentalResearchEnabled: true}, now())
	r, err := r.RequestCancel(now())
	if err != nil {
		t.Fatal(err)
	}
	r, err = r.AcknowledgeCancel(now())
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != StatusCancelled {
		t.Fatalf("unexpected cancel state: %s", r.Status)
	}

	r, _ = New("binding-run", "test bindings", plan(), now())
	if _, err := r.AddArtifact(Artifact{ID: "report", Kind: ArtifactReport, URI: "artifact://binding-run/report", DigestSHA256: strings.Repeat("a", 64), ClaimIDs: []string{"missing"}}, now()); err == nil {
		t.Fatal("artifact with missing claim accepted")
	}
}
