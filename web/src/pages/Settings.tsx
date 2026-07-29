import { useEffect, useState } from "react";

import { api, type Settings } from "../api";
import { Banner } from "../components";

export function SettingsPage() {
  const [settings, setSettings] = useState<Settings | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    api
      .getSettings()
      .then(setSettings)
      .catch((e: unknown) => setError(e instanceof Error ? e.message : "Could not load settings"));
  }, []);

  async function setAI(on: boolean) {
    if (!settings || busy || settings.ai_enabled === on) return;
    const prev = settings;
    setBusy(true);
    setError("");
    setSettings({ ...settings, ai_enabled: on });
    try {
      setSettings(await api.putSettings({ ...settings, ai_enabled: on }));
    } catch (e) {
      setSettings(prev);
      setError(e instanceof Error ? e.message : "Could not save the setting");
    } finally {
      setBusy(false);
    }
  }

  return (
    <main className="narrow">
      <p className="eyebrow">Preferences</p>
      <h1>Settings</h1>
      <p className="lede">Saved on the server and applied immediately.</p>

      {error && <Banner>{error}</Banner>}
      {settings === null && !error && <p className="muted">Loading…</p>}

      {settings && (
        <section className="panel">
          <h3>AI grading</h3>
          <p className="muted">
            On, the AI tutor judges your open-ended answers and writes tailored feedback. Off,
            answers are graded instantly by matching your words against the expected answer — no AI
            calls while you study. “Explain more” always uses the AI, and building decks is
            unaffected either way.
          </p>
          <div className="segment" role="group" aria-label="AI grading">
            <button
              type="button"
              className={settings.ai_enabled ? "on" : ""}
              aria-pressed={settings.ai_enabled}
              disabled={busy}
              onClick={() => void setAI(true)}
            >
              On
            </button>
            <button
              type="button"
              className={settings.ai_enabled ? "" : "on"}
              aria-pressed={!settings.ai_enabled}
              disabled={busy}
              onClick={() => void setAI(false)}
            >
              Off
            </button>
          </div>
        </section>
      )}
    </main>
  );
}
