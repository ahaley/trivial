import { useCallback, useEffect, useRef, useState } from "react";
import { Link, useNavigate } from "react-router-dom";

import { ApiError, api, type TopicSummary } from "../api";
import { Banner, DecayCurve, Empty, Spinner, curveWindow, percent } from "../components";

export function TopicsPage() {
  const [topics, setTopics] = useState<TopicSummary[] | null>(null);
  const [error, setError] = useState("");
  const [composing, setComposing] = useState(false);
  const navigate = useNavigate();

  const load = useCallback(async () => {
    try {
      setTopics(await api.listTopics());
      setError("");
    } catch (e) {
      setError(e instanceof Error ? e.message : "Could not load topics");
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  // While anything is generating, keep the list live so the deck appears the
  // moment it is ready.
  const generating = topics?.some((t) => t.status === "generating") ?? false;
  useEffect(() => {
    if (!generating) return;
    const timer = setInterval(() => void load(), 1500);
    return () => clearInterval(timer);
  }, [generating, load]);

  const dueTotal = topics?.reduce((n, t) => n + t.due_count, 0) ?? 0;

  async function reviewDue() {
    try {
      const session = await api.startSession({});
      navigate(`/sessions/${session.id}`);
    } catch (e) {
      setError(e instanceof ApiError ? e.message : "Could not start a review");
    }
  }

  return (
    <main>
      <p className="eyebrow">Your decks</p>
      <h1>Topics</h1>
      <p className="lede">
        Name a subject and Trivial researches it, writes the questions, and keeps
        track of which ones you keep getting wrong.
      </p>

      {error && <Banner>{error}</Banner>}

      <div className="row" style={{ marginBottom: "2rem" }}>
        <button className="btn" onClick={() => setComposing((v) => !v)}>
          {composing ? "Cancel" : "New topic"}
        </button>
        {dueTotal > 0 && (
          <button className="btn ghost" onClick={() => void reviewDue()}>
            Review {dueTotal} due
          </button>
        )}
      </div>

      {composing && (
        <NewTopicForm
          onCreated={(topic) => {
            setComposing(false);
            setTopics((prev) => (prev ? [topic, ...prev] : [topic]));
          }}
          onError={setError}
        />
      )}

      {topics === null && <p className="muted">Loading…</p>}

      {topics?.length === 0 && !composing && (
        <Empty title="Nothing to study yet">
          <p>Create your first topic and Trivial will build a deck from it.</p>
        </Empty>
      )}

      {topics && topics.length > 0 && (
        <div className="grid">
          {topics.map((t) => (
            <TopicCard key={t.id} topic={t} />
          ))}
        </div>
      )}
    </main>
  );
}

function TopicCard({ topic }: { topic: TopicSummary }) {
  const generating = topic.status === "generating";

  // The curve is the deck's own decay over the coming month: flat once you
  // know it, a cliff when you do not.
  const stability = topic.stability > 0 ? topic.stability : 0.4;

  return (
    <Link to={`/topics/${topic.id}`} className="card">
      {!generating && (
        <DecayCurve
          className="card-curve"
          stability={stability}
          days={curveWindow(stability)}
          height={30}
        />
      )}
      <h3>{topic.subject}</h3>

      {generating ? (
        <>
          <div className="card-meta">
            <span className="pill working">
              <Spinner /> {topic.stage || "starting"}
            </span>
          </div>
          <div className="progress-track">
            <div className="progress-fill" style={{ width: `${topic.progress}%` }} />
          </div>
        </>
      ) : topic.status === "failed" ? (
        <div className="card-meta">
          <span className="pill failed">Failed</span>
          <span>{topic.error}</span>
        </div>
      ) : (
        <div className="card-meta">
          <span>
            <b className="num">{topic.fact_count}</b> questions
          </span>
          <span>
            <b className="num">{percent(topic.mastery)}</b> in memory
          </span>
          {topic.due_count > 0 && <span className="pill due">{topic.due_count} due</span>}
          {topic.due_count === 0 && topic.new_count > 0 && (
            <span className="pill">{topic.new_count} new</span>
          )}
        </div>
      )}
    </Link>
  );
}

function NewTopicForm({
  onCreated,
  onError,
}: {
  onCreated: (t: TopicSummary) => void;
  onError: (msg: string) => void;
}) {
  const [subject, setSubject] = useState("");
  const [guidance, setGuidance] = useState("");
  const [count, setCount] = useState(30);
  const [busy, setBusy] = useState(false);
  const first = useRef<HTMLInputElement>(null);

  useEffect(() => first.current?.focus(), []);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    if (!subject.trim() || busy) return;
    setBusy(true);
    try {
      onCreated(await api.createTopic(subject.trim(), guidance.trim(), count));
    } catch (err) {
      onError(err instanceof Error ? err.message : "Could not create the topic");
    } finally {
      setBusy(false);
    }
  }

  return (
    <form className="panel" onSubmit={submit} style={{ marginBottom: "2rem" }}>
      <label className="field">
        <span>Subject</span>
        <input
          ref={first}
          className="input"
          value={subject}
          onChange={(e) => setSubject(e.target.value)}
          placeholder="The Roman Republic"
          maxLength={200}
          required
        />
      </label>

      <label className="field">
        <span>Direction (optional)</span>
        <input
          className="input"
          value={guidance}
          onChange={(e) => setGuidance(e.target.value)}
          placeholder="Focus on the late republic and the civil wars"
        />
      </label>

      <label className="field">
        <span>Questions</span>
        <input
          className="input"
          type="number"
          min={5}
          max={100}
          value={count}
          onChange={(e) => setCount(Number(e.target.value))}
          style={{ maxWidth: "8rem" }}
        />
      </label>

      <button className="btn" type="submit" disabled={busy || !subject.trim()}>
        {busy ? "Starting…" : "Build the deck"}
      </button>
    </form>
  );
}
