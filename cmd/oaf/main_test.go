package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestResolveHarnessAliases(t *testing.T) {
	harness, label := resolveHarness("codex")
	if harness != "codex_app_server" || label != "Codex" {
		t.Fatalf("codex = %q %q", harness, label)
	}
	harness, label = resolveHarness("claude")
	if harness != "opencode" || label != "Claude" {
		t.Fatalf("claude = %q %q", harness, label)
	}
	if h, _ := resolveHarness("nope"); h != "" {
		t.Fatalf("unknown = %q", h)
	}
}

func TestSquadPresets(t *testing.T) {
	coding, err := squadPreset("coding")
	if err != nil || len(coding.Agents) != 3 {
		t.Fatalf("coding = %#v %v", coding, err)
	}
	research, err := squadPreset("research")
	if err != nil || research.Agents[0].Name != "Research Scout" {
		t.Fatalf("research = %#v %v", research, err)
	}
	if _, err := squadPreset("marketing"); err == nil {
		t.Fatal("expected unknown squad")
	}
}

func TestRaceCreatesGroupAndMentions(t *testing.T) {
	var posts []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		posts = append(posts, r.Method+" "+r.URL.Path+" "+string(body))
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/agents" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"agents":[]}`))
		case r.URL.Path == "/api/agents" && r.Method == http.MethodPost:
			var req map[string]any
			_ = json.Unmarshal(body, &req)
			name, _ := req["name"].(string)
			id := "bot-" + strings.ToLower(name)
			_, _ = w.Write([]byte(`{"bot":{"id":"` + id + `","name":"` + name + `"}}`))
		case r.URL.Path == "/api/groups" && r.Method == http.MethodPost:
			_, _ = w.Write([]byte(`{"group":{"id":"grp-1"}}`))
		case strings.HasSuffix(r.URL.Path, "/messages"):
			_, _ = w.Write([]byte(`{"runs":[{"id":"run-a"},{"id":"run-b"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	client := &apiClient{base: server.URL, http: server.Client(), stdout: io.Discard}
	if err := client.race([]string{"fix all failing tests", "--agents", "grok,codex"}); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(posts, "\n")
	if !strings.Contains(joined, `"harness":"grok_build"`) || !strings.Contains(joined, `"harness":"codex_app_server"`) {
		t.Fatalf("missing harness posts:\n%s", joined)
	}
	if !strings.Contains(joined, "/api/groups/grp-1/messages") {
		t.Fatalf("missing group message:\n%s", joined)
	}
}

func TestRunUsage(t *testing.T) {
	if err := run(nil); err == nil {
		t.Fatal("expected usage")
	}
}
