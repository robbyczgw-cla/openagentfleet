import React, { Component, type ErrorInfo, type ReactNode } from "react";
import ReactDOM from "react-dom/client";
import App from "./App";

class RuntimeErrorBoundary extends Component<
  { children: ReactNode },
  { error: Error | null }
> {
  state = { error: null as Error | null };

  static getDerivedStateFromError(error: Error) {
    return { error };
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    console.error("OpenAgentFleet render failure", error, info);
  }

  render() {
    if (this.state.error) {
      return (
        <main className="loading-screen error-screen" role="alert">
          <span className="orb warning" />
          <div>
            <strong>OpenAgentFleet could not render</strong>
            <p>{this.state.error.message || "Unknown render error"}</p>
            {import.meta.env.DEV && this.state.error.stack && (
              <pre>{this.state.error.stack}</pre>
            )}
          </div>
        </main>
      );
    }
    return this.props.children;
  }
}

ReactDOM.createRoot(document.getElementById("root") as HTMLElement).render(
  <React.StrictMode>
    <RuntimeErrorBoundary>
      <App />
    </RuntimeErrorBoundary>
  </React.StrictMode>,
);
