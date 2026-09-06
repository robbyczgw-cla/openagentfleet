import { FormEvent, useCallback, useEffect, useMemo, useState } from "react";
import "./ConnectedApps.css";

type Agent = { id: string; name: string; harness?: string };

type GitHubStatus = {
  enabled: boolean;
  repositories: string[];
  agent_ids: string[];
  installed: boolean;
  authenticated: boolean;
  login: string;
};

type DiscoveredRepository = { name: string; private: boolean };

type SavedGitHubConnection = Pick<GitHubStatus, "enabled" | "repositories" | "agent_ids" | "login">;

export type ConnectedAppsProps = {
  apiFetch: (path: string, init?: RequestInit) => Promise<Response>;
  agents: Agent[];
  onClose: () => void;
};

async function responseError(response: Response): Promise<string> {
  try {
    const payload = (await response.json()) as { error?: string };
    if (payload.error) return payload.error;
  } catch {
    // The status text still gives the user a usable error when the response is not JSON.
  }
  return response.statusText || `Request failed (${response.status})`;
}

export function ConnectedApps({ apiFetch, agents, onClose }: ConnectedAppsProps) {
  const [status, setStatus] = useState<GitHubStatus | null>(null);
  const [repositories, setRepositories] = useState<DiscoveredRepository[]>([]);
  const [selectedRepositories, setSelectedRepositories] = useState<string[]>([]);
  const [selectedAgents, setSelectedAgents] = useState<string[]>([]);
  const [manualRepository, setManualRepository] = useState("");
  const [defaultHarness, setDefaultHarness] = useState("grok");
  const [loading, setLoading] = useState(true);
  const [discovering, setDiscovering] = useState(false);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");

  const discover = useCallback(async () => {
    setDiscovering(true);
    setError("");
    try {
      const response = await apiFetch("/api/connections/github/repositories");
      if (!response.ok) throw new Error(await responseError(response));
      const payload = (await response.json()) as { repositories?: DiscoveredRepository[] };
      setRepositories(Array.isArray(payload.repositories) ? payload.repositories : []);
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "Could not load GitHub repositories.");
    } finally {
      setDiscovering(false);
    }
  }, [apiFetch]);

  useEffect(() => {
    let active = true;
    async function load() {
      setLoading(true);
      setError("");
      try {
        const [response, preferencesResponse] = await Promise.all([
          apiFetch("/api/connections/github"),
          apiFetch("/api/preferences"),
        ]);
        if (!response.ok) throw new Error(await responseError(response));
        if (!preferencesResponse.ok) throw new Error(await responseError(preferencesResponse));
        const payload = (await response.json()) as GitHubStatus;
        const preferences = (await preferencesResponse.json()) as { workspace?: { engine?: string } };
        if (!active) return;
        setStatus(payload);
        setDefaultHarness(preferences.workspace?.engine || "grok");
        setSelectedRepositories(payload.repositories ?? []);
        setSelectedAgents(payload.agent_ids ?? []);
        if (payload.authenticated) void discover();
      } catch (cause) {
        if (active) setError(cause instanceof Error ? cause.message : "Could not load GitHub status.");
      } finally {
        if (active) setLoading(false);
      }
    }
    void load();
    return () => {
      active = false;
    };
  }, [apiFetch, discover]);

  useEffect(() => {
    const supported = new Set(agents.filter((agent) => (agent.harness || defaultHarness) !== "pi").map((agent) => agent.id));
    setSelectedAgents((current) => current.filter((id) => supported.has(id)));
  }, [agents, defaultHarness]);

  const visibleRepositories = useMemo(() => {
    const byName = new Map<string, DiscoveredRepository>();
    for (const repository of repositories) byName.set(repository.name.toLowerCase(), repository);
    for (const name of selectedRepositories) {
      if (!byName.has(name.toLowerCase())) byName.set(name.toLowerCase(), { name, private: false });
    }
    return [...byName.values()].sort((left, right) => left.name.localeCompare(right.name));
  }, [repositories, selectedRepositories]);

  function toggle(setter: (next: string[]) => void, values: string[], value: string) {
    setter(values.includes(value) ? values.filter((item) => item !== value) : [...values, value]);
  }

  function addManualRepository(event: FormEvent) {
    event.preventDefault();
    const name = manualRepository.trim().toLowerCase();
    if (!/^[a-z0-9][a-z0-9-]{0,38}\/[a-z0-9_.-]{1,100}$/i.test(name) || name.endsWith("/.") || name.endsWith("/..")) {
      setError("Enter a repository as owner/name.");
      return;
    }
    if (!selectedRepositories.includes(name)) setSelectedRepositories((current) => [...current, name]);
    setManualRepository("");
    setError("");
  }

  async function checkAgain() {
    setDiscovering(true);
    setError("");
    try {
      const [response, preferencesResponse] = await Promise.all([
        apiFetch("/api/connections/github"),
        apiFetch("/api/preferences"),
      ]);
      if (!response.ok) throw new Error(await responseError(response));
      if (!preferencesResponse.ok) throw new Error(await responseError(preferencesResponse));
      const payload = (await response.json()) as GitHubStatus;
      const preferences = (await preferencesResponse.json()) as { workspace?: { engine?: string } };
      setStatus(payload);
      setDefaultHarness(preferences.workspace?.engine || "grok");
      setSelectedRepositories(payload.repositories ?? []);
      setSelectedAgents(payload.agent_ids ?? []);
      if (payload.authenticated) await discover();
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "Could not refresh GitHub status.");
    } finally {
      setDiscovering(false);
    }
  }

  async function save(enabled: boolean) {
    setSaving(true);
    setError("");
    try {
      const response = await apiFetch("/api/connections/github", {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          enabled,
          expected_login: enabled ? status?.login ?? "" : "",
          repositories: enabled ? selectedRepositories : [],
          agent_ids: enabled ? selectedAgents : [],
        }),
      });
      if (!response.ok) throw new Error(await responseError(response));
      const saved = (await response.json()) as SavedGitHubConnection;
      setStatus((current) => ({
        ...(current ?? { installed: true, authenticated: enabled, enabled, repositories: [], agent_ids: [], login: "" }),
        ...saved,
        authenticated: enabled ? true : (current?.authenticated ?? false),
        login: enabled ? saved.login : (current?.login ?? ""),
      }));
      if (!enabled) {
        setSelectedRepositories([]);
        setSelectedAgents([]);
        setRepositories([]);
        await checkAgain();
      }
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "Could not save the GitHub connection.");
    } finally {
      setSaving(false);
    }
  }

  const ready = status?.installed && status.authenticated;

  return (
      <section className="connected-apps" aria-labelledby="connected-apps-title">
        <header className="connected-apps-header">
          <div>
            <p>Settings</p>
            <h2 id="connected-apps-title">Connected apps</h2>
          </div>
          <button className="connected-apps-close" type="button" onClick={onClose} aria-label="Close connected apps">×</button>
        </header>

        {loading ? <p className="connected-apps-state">Checking GitHub…</p> : (
          <div className="connected-app-card">
            <div className="connected-app-summary">
              <span className="connected-app-mark" aria-hidden="true">GH</span>
              <div>
                <h3>GitHub</h3>
                <p>
                  {!status?.installed
                    ? "Install the GitHub CLI to connect."
                    : !status.authenticated
                      ? "Run gh auth login on this computer, then refresh."
                      : status.enabled
                        ? `Connected as ${status.login}`
                        : `Authenticated as ${status.login}`}
                </p>
              </div>
              <span className={`connected-app-status ${status?.enabled && status.authenticated ? "is-connected" : ""}`}>
                {status?.enabled && status.authenticated ? "Connected" : "Not connected"}
              </span>
            </div>

            {ready && (
              <div className="connected-app-settings">
                <div className="connected-app-section-heading">
                  <div>
                    <h4>Repository access</h4>
                    <p>Agents can read issues and pull requests only from selected repositories.</p>
                  </div>
                  <button type="button" className="connected-app-secondary" disabled={discovering} onClick={() => void discover()}>
                    {discovering ? "Refreshing…" : "Refresh"}
                  </button>
                </div>

                <div className="connected-app-options">
                  {visibleRepositories.map((repository) => {
                    const normalized = repository.name.toLowerCase();
                    return (
                      <label key={normalized}>
                        <input
                          type="checkbox"
                          checked={selectedRepositories.includes(normalized)}
                          onChange={() => toggle(setSelectedRepositories, selectedRepositories, normalized)}
                        />
                        <span>{repository.name}</span>
                        {repository.private && <small>Private</small>}
                      </label>
                    );
                  })}
                  {!discovering && visibleRepositories.length === 0 && <p className="connected-app-empty">No repositories found. Add one below.</p>}
                </div>

                <form className="connected-app-manual" onSubmit={addManualRepository}>
                  <input value={manualRepository} onChange={(event) => setManualRepository(event.target.value)} placeholder="owner/repository" aria-label="Repository owner and name" />
                  <button className="connected-app-secondary" type="submit">Add</button>
                </form>

                <div className="connected-app-section-heading">
                  <div>
                    <h4>Agent access</h4>
                    <p>Select which Agents receive GitHub read tools.</p>
                  </div>
                </div>
                <div className="connected-app-options connected-app-agents">
                  {agents.map((agent) => (
                    <label key={agent.id} className={(agent.harness || defaultHarness) === "pi" ? "is-disabled" : ""}>
                      <input
                        type="checkbox"
                        checked={selectedAgents.includes(agent.id)}
                        disabled={(agent.harness || defaultHarness) === "pi"}
                        onChange={() => toggle(setSelectedAgents, selectedAgents, agent.id)}
                      />
                      <span>{agent.name}</span>
                      {(agent.harness || defaultHarness) === "pi" && <small>Pi does not support MCP tools</small>}
                    </label>
                  ))}
                  {agents.length === 0 && <p className="connected-app-empty">Create an Agent before granting access.</p>}
                </div>
              </div>
            )}

            {error && <p className="connected-app-error" role="alert">{error}</p>}

            <footer className="connected-app-actions">
              {status?.enabled ? (
                <button className="connected-app-danger" type="button" disabled={saving} onClick={() => void save(false)}>{saving ? "Disconnecting…" : "Disconnect"}</button>
              ) : (
                <button className="connected-app-secondary" type="button" disabled={saving || discovering} onClick={() => void checkAgain()}>{discovering ? "Checking…" : "Check again"}</button>
              )}
              {ready && <button className="connected-app-primary" type="button" disabled={saving} onClick={() => void save(true)}>{saving ? "Saving…" : status?.enabled ? "Save access" : "Enable connection"}</button>}
            </footer>
          </div>
        )}
      </section>
  );
}
