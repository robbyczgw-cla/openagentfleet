package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: oaf run \"<task>\" [--agents grok,codex,claude]\n       oaf squad coding|research")
	}
	client := newClient()
	switch args[0] {
	case "run":
		return client.race(args[1:])
	case "squad":
		if len(args) < 2 {
			return fmt.Errorf("usage: oaf squad coding|research")
		}
		return client.squad(args[1])
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

type apiClient struct {
	base   string
	token  string
	http   *http.Client
	stdout io.Writer
}

func newClient() *apiClient {
	base := strings.TrimSpace(os.Getenv("OPENAGENTFLEET_ADDR"))
	if base == "" {
		base = "http://127.0.0.1:4317"
	}
	if !strings.HasPrefix(base, "http") {
		base = "http://" + base
	}
	return &apiClient{
		base:   strings.TrimRight(base, "/"),
		token:  strings.TrimSpace(os.Getenv("OPENAGENTFLEET_REMOTE_TOKEN")),
		http:   &http.Client{Timeout: 30 * time.Second},
		stdout: os.Stdout,
	}
}

type agentJSON struct {
	Bot struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"bot"`
	Metadata *struct {
		LeadHarness string `json:"lead_harness"`
		Lead        *struct {
			Harness string `json:"harness"`
		} `json:"lead"`
	} `json:"metadata"`
}

func (c *apiClient) race(args []string) error {
	agentsFlag := "grok,codex,claude"
	var taskParts []string
	for i := 0; i < len(args); i++ {
		if args[i] == "--agents" && i+1 < len(args) {
			agentsFlag = args[i+1]
			i++
			continue
		}
		if strings.HasPrefix(args[i], "--agents=") {
			agentsFlag = strings.TrimPrefix(args[i], "--agents=")
			continue
		}
		taskParts = append(taskParts, args[i])
	}
	task := strings.TrimSpace(strings.Join(taskParts, " "))
	if task == "" {
		return fmt.Errorf("usage: oaf run \"<task>\" [--agents grok,codex,claude]")
	}
	wanted := strings.Split(agentsFlag, ",")
	ids := make([]string, 0, len(wanted))
	names := make([]string, 0, len(wanted))
	existing, err := c.listAgents()
	if err != nil {
		return err
	}
	for _, raw := range wanted {
		harness, label := resolveHarness(raw)
		if harness == "" {
			return fmt.Errorf("unknown agent engine %q (grok, codex, claude, gemini, opencode, pi)", raw)
		}
		agent, err := c.findOrCreate(existing, label, harness)
		if err != nil {
			return err
		}
		ids = append(ids, agent.Bot.ID)
		names = append(names, agent.Bot.Name)
		existing = append(existing, agent)
	}
	if len(ids) < 2 {
		return fmt.Errorf("need at least two --agents for a race")
	}
	group, err := c.createGroup("race: "+truncate(task, 80), ids)
	if err != nil {
		return err
	}
	mentions := make([]string, 0, len(names))
	for _, name := range names {
		mentions = append(mentions, "@"+name)
	}
	content := strings.Join(mentions, " ") + " " + task + "\n\nRace: each agent works the same task. Keep a patch small. Stop when tests pass."
	runs, err := c.postGroup(group, content, ids)
	if err != nil {
		return err
	}
	fmt.Fprintf(c.stdout, "group %s\n", group)
	for i, name := range names {
		runID := ""
		if i < len(runs) {
			runID = runs[i]
		}
		fmt.Fprintf(c.stdout, "  %s  %s  run %s\n", name, ids[i], runID)
	}
	fmt.Fprintf(c.stdout, "open the OpenAgentFleet window to watch live status.\n")
	return nil
}

type squadSpec struct {
	Title  string
	Agents []struct {
		Name, Title, Description, Harness string
	}
}

func squadPreset(kind string) (squadSpec, error) {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "coding":
		return squadSpec{
			Title: "coding",
			Agents: []struct{ Name, Title, Description, Harness string }{
				{"Builder", "Builds reliable software", "Pragmatic implementation, testing, and trade-offs.", "grok_build"},
				{"Fast Ops", "Moves work forward quickly", "Confirm the target and take the next useful step.", "codex_app_server"},
				{"Fleet Lead", "Coordinates ownership", "Plan, prioritize, keep moving parts aligned.", "opencode"},
			},
		}, nil
	case "research":
		return squadSpec{
			Title: "research",
			Agents: []struct{ Name, Title, Description, Harness string }{
				{"Research Scout", "Finds trustworthy evidence", "Source-aware investigation with clear confidence.", "grok_build"},
				{"Writer", "Shapes clear writing", "Turn rough thinking into audience-aware prose.", "opencode"},
				{"Data Analyst", "Turns data into insight", "Methodical analysis, honest uncertainty.", "codex_app_server"},
			},
		}, nil
	default:
		return squadSpec{}, fmt.Errorf("unknown squad %q (coding|research)", kind)
	}
}

func (c *apiClient) squad(kind string) error {
	spec, err := squadPreset(kind)
	if err != nil {
		return err
	}
	existing, err := c.listAgents()
	if err != nil {
		return err
	}
	ids := make([]string, 0, len(spec.Agents))
	for _, member := range spec.Agents {
		agent, err := c.findOrCreateNamed(existing, member.Name, member.Title, member.Description, member.Harness)
		if err != nil {
			return err
		}
		ids = append(ids, agent.Bot.ID)
		existing = append(existing, agent)
		fmt.Fprintf(c.stdout, "agent %s %s (%s)\n", agent.Bot.Name, agent.Bot.ID, member.Harness)
	}
	group, err := c.createGroup("squad "+spec.Title, ids)
	if err != nil {
		return err
	}
	fmt.Fprintf(c.stdout, "squad %s group %s\n", spec.Title, group)
	return nil
}

func resolveHarness(raw string) (harness, label string) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "grok", "grok_build":
		return "grok_build", "Grok"
	case "codex", "chatgpt", "codex_app_server":
		return "codex_app_server", "Codex"
	case "claude", "gemini", "opencode":
		label := strings.ToLower(strings.TrimSpace(raw))
		if label == "" {
			label = "opencode"
		}
		return "opencode", strings.ToUpper(label[:1]) + label[1:]
	case "pi":
		return "pi", "Pi"
	default:
		return "", ""
	}
}

func (c *apiClient) findOrCreate(existing []agentJSON, label, harness string) (agentJSON, error) {
	for _, agent := range existing {
		if harnessOf(agent) == harness && strings.EqualFold(agent.Bot.Name, label) {
			return agent, nil
		}
	}
	desc := "Uses your existing " + label + " sign-in. No API-key orchestrator."
	return c.createAgent(label, label+" teammate", desc, harness)
}

func (c *apiClient) findOrCreateNamed(existing []agentJSON, name, title, description, harness string) (agentJSON, error) {
	for _, agent := range existing {
		if strings.EqualFold(agent.Bot.Name, name) {
			return agent, nil
		}
	}
	return c.createAgent(name, title, description, harness)
}

func harnessOf(agent agentJSON) string {
	if agent.Metadata == nil {
		return ""
	}
	if agent.Metadata.Lead != nil && agent.Metadata.Lead.Harness != "" {
		return agent.Metadata.Lead.Harness
	}
	return agent.Metadata.LeadHarness
}

func (c *apiClient) listAgents() ([]agentJSON, error) {
	var payload struct {
		Agents []agentJSON `json:"agents"`
	}
	if err := c.do(http.MethodGet, "/api/agents", nil, &payload); err != nil {
		return nil, err
	}
	return payload.Agents, nil
}

func (c *apiClient) createAgent(name, title, description, harness string) (agentJSON, error) {
	body := map[string]any{
		"name":        name,
		"title":       title,
		"description": description,
		"metadata": map[string]any{
			"lead": map[string]any{"harness": harness},
		},
	}
	var created agentJSON
	if err := c.do(http.MethodPost, "/api/agents", body, &created); err != nil {
		return agentJSON{}, err
	}
	return created, nil
}

func (c *apiClient) createGroup(title string, agentIDs []string) (string, error) {
	var payload struct {
		Group struct {
			ID string `json:"id"`
		} `json:"group"`
	}
	if err := c.do(http.MethodPost, "/api/groups", map[string]any{"title": title, "agent_ids": agentIDs}, &payload); err != nil {
		return "", err
	}
	return payload.Group.ID, nil
}

func (c *apiClient) postGroup(groupID, content string, mentionIDs []string) ([]string, error) {
	var payload struct {
		Runs []struct {
			ID string `json:"id"`
		} `json:"runs"`
	}
	if err := c.do(http.MethodPost, "/api/groups/"+groupID+"/messages", map[string]any{
		"content":         content,
		"mention_bot_ids": mentionIDs,
	}, &payload); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(payload.Runs))
	for _, run := range payload.Runs {
		ids = append(ids, run.ID)
	}
	return ids, nil
}

func (c *apiClient) do(method, path string, body any, dest any) error {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, c.base+path, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("botd %s %s: %w (is OpenAgentFleet running?)", method, path, err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		return err
	}
	if res.StatusCode >= 300 {
		return fmt.Errorf("botd %s %s: %s %s", method, path, res.Status, truncate(string(raw), 240))
	}
	if dest == nil {
		return nil
	}
	return json.Unmarshal(raw, dest)
}

func truncate(value string, n int) string {
	if len(value) <= n {
		return value
	}
	return value[:n] + "…"
}
