package store

import (
	"path/filepath"
	"testing"

	"github.com/robbyczgw-cla/openagentfleet/internal/domain"
)

func TestListLatestRunsByBotKeepsNewestPerAgent(t *testing.T) {
	instance, err := Open(filepath.Join(t.TempDir(), "presence.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = instance.Close() })
	ctx := t.Context()
	first, err := instance.CreateAgent(ctx, domain.AgentDraft{Name: "Andy", Title: "Builder", Description: ""})
	if err != nil {
		t.Fatal(err)
	}
	second, err := instance.CreateAgent(ctx, domain.AgentDraft{Name: "Cami", Title: "Reviewer", Description: ""})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := instance.CreateRun(ctx, first.Conversation.ID, first.Bot.ID, "grok", "old"); err != nil {
		t.Fatal(err)
	}
	newer, err := instance.CreateRun(ctx, first.Conversation.ID, first.Bot.ID, "grok", "new")
	if err != nil {
		t.Fatal(err)
	}
	if err := instance.UpdateRun(ctx, newer.ID, "running", ""); err != nil {
		t.Fatal(err)
	}
	camiRun, err := instance.CreateRun(ctx, second.Conversation.ID, second.Bot.ID, "codex_app_server", "review")
	if err != nil {
		t.Fatal(err)
	}
	if err := instance.UpdateRun(ctx, camiRun.ID, "waiting_for_approval", ""); err != nil {
		t.Fatal(err)
	}

	runs, err := instance.ListLatestRunsByBot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 {
		t.Fatalf("latest runs = %#v", runs)
	}
	byBot := map[string]domain.Run{}
	for _, run := range runs {
		byBot[run.BotID] = run
	}
	if byBot[first.Bot.ID].ID != newer.ID || byBot[first.Bot.ID].Status != "running" {
		t.Fatalf("andy run = %#v", byBot[first.Bot.ID])
	}
	if byBot[second.Bot.ID].Status != "waiting_for_approval" {
		t.Fatalf("cami run = %#v", byBot[second.Bot.ID])
	}
}

func TestListLatestTerminalRunsByBotSkipsInFlightWork(t *testing.T) {
	instance, err := Open(filepath.Join(t.TempDir(), "presence-terminal.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = instance.Close() })
	ctx := t.Context()
	agent, err := instance.CreateAgent(ctx, domain.AgentDraft{Name: "Andy", Title: "Builder", Description: ""})
	if err != nil {
		t.Fatal(err)
	}
	finished, err := instance.CreateRun(ctx, agent.Conversation.ID, agent.Bot.ID, "grok", "done")
	if err != nil {
		t.Fatal(err)
	}
	if err := instance.UpdateRun(ctx, finished.ID, "completed", ""); err != nil {
		t.Fatal(err)
	}
	newer, err := instance.CreateRun(ctx, agent.Conversation.ID, agent.Bot.ID, "grok", "in flight")
	if err != nil {
		t.Fatal(err)
	}
	if err := instance.UpdateRun(ctx, newer.ID, "running", ""); err != nil {
		t.Fatal(err)
	}

	latest, err := instance.ListLatestRunsByBot(ctx)
	if err != nil || len(latest) != 1 || latest[0].ID != newer.ID {
		t.Fatalf("latest runs = %#v, err = %v", latest, err)
	}
	terminal, err := instance.ListLatestTerminalRunsByBot(ctx)
	if err != nil || len(terminal) != 1 || terminal[0].ID != finished.ID || terminal[0].Status != "completed" {
		t.Fatalf("terminal runs = %#v, err = %v", terminal, err)
	}

	blocked, err := instance.CreateRun(ctx, agent.Conversation.ID, agent.Bot.ID, "grok", "blocked")
	if err != nil {
		t.Fatal(err)
	}
	if err := instance.UpdateRun(ctx, blocked.ID, "blocked", "harness execution is disabled"); err != nil {
		t.Fatal(err)
	}
	terminal, err = instance.ListLatestTerminalRunsByBot(ctx)
	if err != nil || len(terminal) != 1 || terminal[0].ID != blocked.ID || terminal[0].Status != "blocked" {
		t.Fatalf("blocked terminal runs = %#v, err = %v", terminal, err)
	}
}
