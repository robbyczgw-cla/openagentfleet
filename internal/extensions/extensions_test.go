package extensions

import (
	"strings"
	"testing"
	"time"
)

func fixedTime() time.Time { return time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC) }

func validManifest() Manifest {
	return Manifest{SchemaVersion: CurrentManifestVersion, ID: "example.github", Kind: KindConnector, Name: "Example GitHub", Version: "1.2.3", Capabilities: []string{"mcp.tools", "oauth"}, SecretRefs: []SecretRef{{Name: "github.token", Required: true}}, Provenance: Provenance{OriginURL: "https://github.com/example/connector/releases/tag/v1.2.3", Publisher: "Example", DigestSHA256: strings.Repeat("a", 64), License: "MIT", LicenseURL: "https://opensource.org/license/mit", Verified: true}}
}

func TestInstallIsDisabledAndAuditable(t *testing.T) {
	e, err := Install(validManifest(), Policy{}, fixedTime())
	if err != nil {
		t.Fatal(err)
	}
	if e.State != StateInstalled || e.Enabled || e.Revision != 1 {
		t.Fatalf("unexpected install state: %#v", e)
	}
	if len(e.Audit) != 1 || e.Audit[0].Action != "installed" {
		t.Fatalf("missing audit: %#v", e.Audit)
	}
}

func TestEnableNeedsExplicitExperimentalToggle(t *testing.T) {
	e, _ := Install(validManifest(), Policy{}, fixedTime())
	if _, err := e.Enable(Policy{}, fixedTime()); err == nil {
		t.Fatal("expected deny-first enable")
	}
	enabled, err := e.Enable(Policy{ExperimentalExtensionsEnabled: true}, fixedTime())
	if err != nil {
		t.Fatal(err)
	}
	if !enabled.Enabled || enabled.State != StateEnabled {
		t.Fatalf("not enabled: %#v", enabled)
	}
}

func TestUnverifiedProvenanceNeedsExplicitPolicy(t *testing.T) {
	m := validManifest()
	m.Provenance.Verified = false
	if _, err := Install(m, Policy{}, fixedTime()); err == nil {
		t.Fatal("expected unverified provenance rejection")
	}
	if _, err := Install(m, Policy{AllowUnverifiedProvenance: true}, fixedTime()); err != nil {
		t.Fatal(err)
	}
}

func TestManifestValidationRejectsFloatingVersionSecretsAndUnsafeSource(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Manifest)
	}{
		{"floating version", func(m *Manifest) { m.Version = "latest" }},
		{"secret shaped ref", func(m *Manifest) { m.SecretRefs[0].Name = "token=actual-secret" }},
		{"private source", func(m *Manifest) { m.Provenance.OriginURL = "https://127.0.0.1/extension" }},
		{"duplicate capability", func(m *Manifest) { m.Capabilities = append(m.Capabilities, "oauth") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := validManifest()
			tc.mutate(&m)
			if err := m.Validate(Policy{}); err == nil {
				t.Fatal("expected rejection")
			}
		})
	}
}

func TestUpdateDisablesAndPreservesIdentity(t *testing.T) {
	e, _ := Install(validManifest(), Policy{}, fixedTime())
	e, _ = e.Enable(Policy{ExperimentalExtensionsEnabled: true}, fixedTime())
	candidate := validManifest()
	candidate.Version = "1.2.4"
	candidate.Provenance.DigestSHA256 = strings.Repeat("b", 64)
	plan, err := e.PlanUpdate(candidate, Policy{}, fixedTime())
	if err != nil {
		t.Fatal(err)
	}
	updated, err := e.ApplyUpdate(plan, Policy{}, fixedTime())
	if err != nil {
		t.Fatal(err)
	}
	if updated.Enabled || updated.State != StateDisabled || updated.Manifest.Version != "1.2.4" {
		t.Fatalf("unsafe update state: %#v", updated)
	}
	if _, err := updated.Enable(Policy{ExperimentalExtensionsEnabled: true}, fixedTime()); err != nil {
		t.Fatal(err)
	}
}

func TestHealthAndUninstallAreMetadataOnly(t *testing.T) {
	e, _ := Install(validManifest(), Policy{}, fixedTime())
	e, err := e.RecordHealth(HealthReport{State: HealthHealthy, Detail: "reported by external adapter"}, fixedTime())
	if err != nil {
		t.Fatal(err)
	}
	if e.Health.State != HealthHealthy {
		t.Fatalf("health missing: %#v", e.Health)
	}
	e = e.Uninstall(fixedTime())
	if e.Enabled || e.State != StateUninstalled || e.Manifest.ID != "example.github" {
		t.Fatalf("uninstall must retain audit identity: %#v", e)
	}
	if _, err := e.Enable(Policy{ExperimentalExtensionsEnabled: true}, fixedTime()); err == nil {
		t.Fatal("uninstalled extension became enabled")
	}
}
