// Package research models source-backed research runs without performing any
// web requests or invoking a model. It is a validation/audit core for a future
// optional Research Run executor.
package research

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
)

const CurrentVersion = 1

var (
	identifierPattern = regexp.MustCompile(`^[a-z][a-z0-9]*(?:[-_][a-z0-9]+)*$`)
	digestPattern     = regexp.MustCompile(`^[a-f0-9]{64}$`)
)

type Status string

const (
	StatusDraft           Status = "draft"
	StatusQueued          Status = "queued"
	StatusRunning         Status = "running"
	StatusNeedsAttention  Status = "needs_attention"
	StatusCompleted       Status = "completed"
	StatusCancelRequested Status = "cancel_requested"
	StatusCancelled       Status = "cancelled"
	StatusFailed          Status = "failed"
)

type EvidenceKind string

const (
	EvidenceVerified  EvidenceKind = "verified"
	EvidenceInference EvidenceKind = "inference"
)

type SourceKind string

const (
	SourcePrimary   SourceKind = "primary"
	SourceSecondary SourceKind = "secondary"
	SourceUserFile  SourceKind = "user_file"
)

type ArtifactKind string

const (
	ArtifactReport ArtifactKind = "report"
	ArtifactTable  ArtifactKind = "table"
	ArtifactNotes  ArtifactKind = "notes"
)

// Policy defaults to disabled. A future daemon must opt in at settings level
// before it permits a runner to transition a run to queued/running.
type Policy struct {
	ExperimentalResearchEnabled bool `json:"experimental_research_enabled"`
	NetworkFetchEnabled         bool `json:"network_fetch_enabled"`
}

// WorkPlan deliberately bounds any future execution. It is data only; no
// goroutine, model call, browser, or HTTP fetch is created by this package.
type WorkPlan struct {
	MaxSources         uint8  `json:"max_sources"`
	MaxClaims          uint16 `json:"max_claims"`
	MaxArtifacts       uint8  `json:"max_artifacts"`
	MaxDurationSeconds uint32 `json:"max_duration_seconds"`
	AllowNetwork       bool   `json:"allow_network"`
}

type Source struct {
	ID                  string     `json:"id"`
	Kind                SourceKind `json:"kind"`
	URL                 string     `json:"url"`
	Title               string     `json:"title"`
	Publisher           string     `json:"publisher,omitempty"`
	RetrievedAt         time.Time  `json:"retrieved_at"`
	ContentDigestSHA256 string     `json:"content_digest_sha256,omitempty"`
}

// Citation always binds to one source. Locator can be a page/section/table
// reference but not an executable URL or arbitrary HTML.
type Citation struct {
	ID       string `json:"id"`
	SourceID string `json:"source_id"`
	Locator  string `json:"locator,omitempty"`
	Note     string `json:"note,omitempty"`
}

// Claim separates directly supported statements from inferences. A verified
// claim requires at least one citation. An inference must say what reasoning
// was used and still requires cited evidence.
type Claim struct {
	ID             string       `json:"id"`
	Text           string       `json:"text"`
	Evidence       EvidenceKind `json:"evidence"`
	CitationIDs    []string     `json:"citation_ids"`
	InferenceBasis string       `json:"inference_basis,omitempty"`
}

// Artifact holds only a managed artifact URI and its hash. The data itself is
// outside this model so it cannot accidentally become a second source of truth.
type Artifact struct {
	ID           string       `json:"id"`
	Kind         ArtifactKind `json:"kind"`
	URI          string       `json:"uri"`
	DigestSHA256 string       `json:"digest_sha256"`
	ClaimIDs     []string     `json:"claim_ids"`
	SourceIDs    []string     `json:"source_ids"`
}

type Event struct {
	Type   string    `json:"type"`
	Detail string    `json:"detail,omitempty"`
	At     time.Time `json:"at"`
}

// Run is a single bounded, source-backed work item. IDs are caller supplied
// so storage layers can choose their own ID strategy.
type Run struct {
	Version   int        `json:"version"`
	ID        string     `json:"id"`
	Query     string     `json:"query"`
	Plan      WorkPlan   `json:"plan"`
	Status    Status     `json:"status"`
	Sources   []Source   `json:"sources"`
	Citations []Citation `json:"citations"`
	Claims    []Claim    `json:"claims"`
	Artifacts []Artifact `json:"artifacts"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	Events    []Event    `json:"events"`
}

func New(id, query string, plan WorkPlan, now time.Time) (Run, error) {
	if !identifierPattern.MatchString(id) {
		return Run{}, fmt.Errorf("invalid research run id %q", id)
	}
	if err := validateQuery(query); err != nil {
		return Run{}, err
	}
	if err := plan.Validate(); err != nil {
		return Run{}, err
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	return Run{Version: CurrentVersion, ID: id, Query: strings.TrimSpace(query), Plan: plan, Status: StatusDraft, CreatedAt: now, UpdatedAt: now, Events: []Event{{Type: "created", At: now}}}, nil
}

func (p WorkPlan) Validate() error {
	if p.MaxSources < 1 || p.MaxSources > 20 {
		return errors.New("max_sources must be between 1 and 20")
	}
	if p.MaxClaims < 1 || p.MaxClaims > 100 {
		return errors.New("max_claims must be between 1 and 100")
	}
	if p.MaxArtifacts > 10 {
		return errors.New("max_artifacts must not exceed 10")
	}
	if p.MaxDurationSeconds < 30 || p.MaxDurationSeconds > 3600 {
		return errors.New("max_duration_seconds must be between 30 and 3600")
	}
	return nil
}

// Queue and Start are lifecycle-only operations. Both require an explicit
// experiment toggle. Start additionally requires a network setting only when
// the submitted work plan requested network access.
func (r Run) Queue(policy Policy, now time.Time) (Run, error) {
	if !policy.ExperimentalResearchEnabled {
		return Run{}, errors.New("research runs are experimental and disabled by policy")
	}
	if r.Status != StatusDraft && r.Status != StatusNeedsAttention {
		return Run{}, fmt.Errorf("cannot queue a run in state %q", r.Status)
	}
	return r.transition(StatusQueued, "queued", "awaiting external runner", now), nil
}

func (r Run) Start(policy Policy, now time.Time) (Run, error) {
	if !policy.ExperimentalResearchEnabled {
		return Run{}, errors.New("research runs are experimental and disabled by policy")
	}
	if r.Status != StatusQueued {
		return Run{}, fmt.Errorf("cannot start a run in state %q", r.Status)
	}
	if r.Plan.AllowNetwork && !policy.NetworkFetchEnabled {
		return Run{}, errors.New("network research is disabled by policy")
	}
	return r.transition(StatusRunning, "started", "external runner owns execution", now), nil
}

func (r Run) RequestCancel(now time.Time) (Run, error) {
	if r.Status != StatusQueued && r.Status != StatusRunning && r.Status != StatusNeedsAttention {
		return Run{}, fmt.Errorf("cannot cancel a run in state %q", r.Status)
	}
	return r.transition(StatusCancelRequested, "cancel_requested", "external runner must acknowledge", now), nil
}

func (r Run) AcknowledgeCancel(now time.Time) (Run, error) {
	if r.Status != StatusCancelRequested {
		return Run{}, fmt.Errorf("cannot acknowledge cancellation in state %q", r.Status)
	}
	return r.transition(StatusCancelled, "cancelled", "external runner acknowledged", now), nil
}

func (r Run) NeedsAttention(detail string, now time.Time) (Run, error) {
	if r.Status != StatusRunning {
		return Run{}, fmt.Errorf("cannot request attention in state %q", r.Status)
	}
	if strings.TrimSpace(detail) == "" || len(detail) > 1000 {
		return Run{}, errors.New("attention detail must be 1-1000 characters")
	}
	return r.transition(StatusNeedsAttention, "needs_attention", detail, now), nil
}

func (r Run) Fail(detail string, now time.Time) (Run, error) {
	if r.Status != StatusRunning && r.Status != StatusNeedsAttention && r.Status != StatusCancelRequested {
		return Run{}, fmt.Errorf("cannot fail a run in state %q", r.Status)
	}
	if strings.TrimSpace(detail) == "" || len(detail) > 1000 {
		return Run{}, errors.New("failure detail must be 1-1000 characters")
	}
	return r.transition(StatusFailed, "failed", detail, now), nil
}

func (r Run) AddSource(source Source, now time.Time) (Run, error) {
	if err := r.mutable(); err != nil {
		return Run{}, err
	}
	if len(r.Sources) >= int(r.Plan.MaxSources) {
		return Run{}, errors.New("source budget exhausted")
	}
	if err := source.Validate(); err != nil {
		return Run{}, err
	}
	if r.hasSource(source.ID) {
		return Run{}, fmt.Errorf("duplicate source id %q", source.ID)
	}
	r.Sources = append(r.Sources, source)
	return r.touch("source_added", source.ID, now), nil
}

func (r Run) AddCitation(citation Citation, now time.Time) (Run, error) {
	if err := r.mutable(); err != nil {
		return Run{}, err
	}
	if !identifierPattern.MatchString(citation.ID) {
		return Run{}, fmt.Errorf("invalid citation id %q", citation.ID)
	}
	if !r.hasSource(citation.SourceID) {
		return Run{}, fmt.Errorf("citation %q references unknown source %q", citation.ID, citation.SourceID)
	}
	if len(citation.Locator) > 400 || len(citation.Note) > 600 {
		return Run{}, errors.New("citation locator or note exceeds bound")
	}
	if r.hasCitation(citation.ID) {
		return Run{}, fmt.Errorf("duplicate citation id %q", citation.ID)
	}
	r.Citations = append(r.Citations, citation)
	return r.touch("citation_added", citation.ID, now), nil
}

func (r Run) AddClaim(claim Claim, now time.Time) (Run, error) {
	if err := r.mutable(); err != nil {
		return Run{}, err
	}
	if len(r.Claims) >= int(r.Plan.MaxClaims) {
		return Run{}, errors.New("claim budget exhausted")
	}
	if err := r.validateClaim(claim); err != nil {
		return Run{}, err
	}
	if r.hasClaim(claim.ID) {
		return Run{}, fmt.Errorf("duplicate claim id %q", claim.ID)
	}
	claim.CitationIDs = uniqueSorted(claim.CitationIDs)
	r.Claims = append(r.Claims, claim)
	return r.touch("claim_added", claim.ID, now), nil
}

func (r Run) AddArtifact(artifact Artifact, now time.Time) (Run, error) {
	if err := r.mutable(); err != nil {
		return Run{}, err
	}
	if len(r.Artifacts) >= int(r.Plan.MaxArtifacts) {
		return Run{}, errors.New("artifact budget exhausted")
	}
	if err := r.validateArtifact(artifact); err != nil {
		return Run{}, err
	}
	if r.hasArtifact(artifact.ID) {
		return Run{}, fmt.Errorf("duplicate artifact id %q", artifact.ID)
	}
	artifact.ClaimIDs, artifact.SourceIDs = uniqueSorted(artifact.ClaimIDs), uniqueSorted(artifact.SourceIDs)
	r.Artifacts = append(r.Artifacts, artifact)
	return r.touch("artifact_added", artifact.ID, now), nil
}

func (r Run) Complete(now time.Time) (Run, error) {
	if r.Status != StatusRunning && r.Status != StatusNeedsAttention {
		return Run{}, fmt.Errorf("cannot complete a run in state %q", r.Status)
	}
	if len(r.Claims) == 0 {
		return Run{}, errors.New("a completed research run needs at least one claim")
	}
	if len(r.Citations) == 0 {
		return Run{}, errors.New("a completed research run needs at least one citation")
	}
	if err := r.ValidateBindings(); err != nil {
		return Run{}, err
	}
	return r.transition(StatusCompleted, "completed", "source bindings validated", now), nil
}

func (s Source) Validate() error {
	if !identifierPattern.MatchString(s.ID) {
		return fmt.Errorf("invalid source id %q", s.ID)
	}
	if s.Kind != SourcePrimary && s.Kind != SourceSecondary && s.Kind != SourceUserFile {
		return fmt.Errorf("invalid source kind %q", s.Kind)
	}
	if err := ValidatePublicHTTPSURL(s.URL); err != nil {
		return fmt.Errorf("source URL: %w", err)
	}
	if strings.TrimSpace(s.Title) == "" || len(s.Title) > 400 {
		return errors.New("source title must be 1-400 characters")
	}
	if len(s.Publisher) > 200 {
		return errors.New("source publisher exceeds 200 characters")
	}
	if s.RetrievedAt.IsZero() {
		return errors.New("source retrieved_at is required")
	}
	if s.ContentDigestSHA256 != "" && !digestPattern.MatchString(s.ContentDigestSHA256) {
		return errors.New("source digest must be lowercase SHA-256")
	}
	return nil
}

func (r Run) ValidateBindings() error {
	if r.Version != CurrentVersion {
		return fmt.Errorf("unsupported research run version %d", r.Version)
	}
	if err := r.Plan.Validate(); err != nil {
		return err
	}
	if len(r.Sources) > int(r.Plan.MaxSources) || len(r.Claims) > int(r.Plan.MaxClaims) || len(r.Artifacts) > int(r.Plan.MaxArtifacts) {
		return errors.New("run exceeds its work plan budget")
	}
	for _, source := range r.Sources {
		if err := source.Validate(); err != nil {
			return err
		}
	}
	for _, citation := range r.Citations {
		if !r.hasSource(citation.SourceID) {
			return fmt.Errorf("citation %q has an unknown source", citation.ID)
		}
	}
	for _, claim := range r.Claims {
		if err := r.validateClaim(claim); err != nil {
			return err
		}
	}
	for _, artifact := range r.Artifacts {
		if err := r.validateArtifact(artifact); err != nil {
			return err
		}
	}
	return nil
}

func (r Run) validateClaim(claim Claim) error {
	if !identifierPattern.MatchString(claim.ID) {
		return fmt.Errorf("invalid claim id %q", claim.ID)
	}
	if strings.TrimSpace(claim.Text) == "" || len(claim.Text) > 4000 {
		return errors.New("claim text must be 1-4000 characters")
	}
	if claim.Evidence != EvidenceVerified && claim.Evidence != EvidenceInference {
		return fmt.Errorf("invalid evidence kind %q", claim.Evidence)
	}
	if len(claim.CitationIDs) == 0 {
		return errors.New("every claim requires at least one citation")
	}
	if len(claim.CitationIDs) > 16 {
		return errors.New("claim has too many citations")
	}
	seen := map[string]struct{}{}
	for _, id := range claim.CitationIDs {
		if !r.hasCitation(id) {
			return fmt.Errorf("claim %q references unknown citation %q", claim.ID, id)
		}
		if _, ok := seen[id]; ok {
			return fmt.Errorf("claim %q duplicates citation %q", claim.ID, id)
		}
		seen[id] = struct{}{}
	}
	if claim.Evidence == EvidenceVerified && claim.InferenceBasis != "" {
		return errors.New("verified claim cannot include inference_basis")
	}
	if claim.Evidence == EvidenceInference && (strings.TrimSpace(claim.InferenceBasis) == "" || len(claim.InferenceBasis) > 1000) {
		return errors.New("inference claim requires a bounded inference_basis")
	}
	return nil
}

func (r Run) validateArtifact(artifact Artifact) error {
	if !identifierPattern.MatchString(artifact.ID) {
		return fmt.Errorf("invalid artifact id %q", artifact.ID)
	}
	if artifact.Kind != ArtifactReport && artifact.Kind != ArtifactTable && artifact.Kind != ArtifactNotes {
		return fmt.Errorf("invalid artifact kind %q", artifact.Kind)
	}
	if err := validateArtifactURI(artifact.URI, r.ID, artifact.ID); err != nil {
		return err
	}
	if !digestPattern.MatchString(artifact.DigestSHA256) {
		return errors.New("artifact digest must be lowercase SHA-256")
	}
	if len(artifact.ClaimIDs) == 0 {
		return errors.New("artifact requires at least one claim binding")
	}
	for _, id := range artifact.ClaimIDs {
		if !r.hasClaim(id) {
			return fmt.Errorf("artifact %q references unknown claim %q", artifact.ID, id)
		}
	}
	for _, id := range artifact.SourceIDs {
		if !r.hasSource(id) {
			return fmt.Errorf("artifact %q references unknown source %q", artifact.ID, id)
		}
	}
	return nil
}

func (r Run) mutable() error {
	if r.Status == StatusCompleted || r.Status == StatusCancelled || r.Status == StatusFailed {
		return fmt.Errorf("research run is immutable in state %q", r.Status)
	}
	return nil
}
func (r Run) hasSource(id string) bool {
	for _, source := range r.Sources {
		if source.ID == id {
			return true
		}
	}
	return false
}
func (r Run) hasCitation(id string) bool {
	for _, citation := range r.Citations {
		if citation.ID == id {
			return true
		}
	}
	return false
}
func (r Run) hasClaim(id string) bool {
	for _, claim := range r.Claims {
		if claim.ID == id {
			return true
		}
	}
	return false
}
func (r Run) hasArtifact(id string) bool {
	for _, artifact := range r.Artifacts {
		if artifact.ID == id {
			return true
		}
	}
	return false
}
func (r Run) touch(kind, detail string, now time.Time) Run {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	r.UpdatedAt = now
	r.Events = append(r.Events, Event{Type: kind, Detail: detail, At: now})
	return r
}
func (r Run) transition(status Status, kind, detail string, now time.Time) Run {
	r.Status = status
	return r.touch(kind, detail, now)
}

func validateQuery(query string) error {
	q := strings.TrimSpace(query)
	if q == "" || len(q) > 2000 {
		return errors.New("research query must be 1-2000 characters")
	}
	return nil
}
func validateArtifactURI(raw, runID, artifactID string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "artifact" || u.Host != runID || strings.TrimPrefix(u.Path, "/") != artifactID || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("artifact URI must be artifact://%s/%s", runID, artifactID)
	}
	return nil
}

// ValidatePublicHTTPSURL uses the same safe URL contract as the extension
// core but stays dependency-free. It does not resolve DNS; the eventual fetch
// layer must enforce DNS-aware egress policy and redirects separately.
func ValidatePublicHTTPSURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return errors.New("invalid URL")
	}
	if u.Scheme != "https" || u.Hostname() == "" || u.User != nil {
		return errors.New("must be a public HTTPS URL without user info")
	}
	if u.Port() != "" && u.Port() != "443" {
		return errors.New("only default HTTPS port is allowed")
	}
	if ip := net.ParseIP(u.Hostname()); ip != nil && (ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified()) {
		return errors.New("private or loopback address is not allowed")
	}
	return nil
}

func uniqueSorted(values []string) []string {
	copy := append([]string(nil), values...)
	sort.Strings(copy)
	if len(copy) == 0 {
		return copy
	}
	out := copy[:1]
	for _, value := range copy[1:] {
		if value != out[len(out)-1] {
			out = append(out, value)
		}
	}
	return out
}

// DigestContent exposes the canonical SHA-256 contract for an external
// artifact writer without accepting a path or opening a file itself.
func DigestContent(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}
