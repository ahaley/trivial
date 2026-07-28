import { useCallback, useEffect, useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";

import { ApiError, api, type Fact, type TopicDetail } from "../api";
import {
  Banner,
  DecayCurve,
  Empty,
  Spinner,
  curveWindow,
  percent,
  recallColor,
  relativeDay,
} from "../components";

export function TopicPage() {
  const { id } = useParams();
  const topicId = Number(id);
  const navigate = useNavigate();

  const [topic, setTopic] = useState<TopicDetail | null>(null);
  const [error, setError] = useState("");
  const [starting, setStarting] = useState(false);
  const [regenerating, setRegenerating] = useState(false);

  const load = useCallback(async () => {
    try {
      setTopic(await api.getTopic(topicId));
      setError("");
    } catch (e) {
      setError(e instanceof Error ? e.message : "Could not load this topic");
    }
  }, [topicId]);

  useEffect(() => {
    void load();
  }, [load]);

  const generating = topic?.status === "generating";
  useEffect(() => {
    if (!generating) return;
    const timer = setInterval(() => void load(), 1200);
    return () => clearInterval(timer);
  }, [generating, load]);

  async function start(tag?: string) {
    setStarting(true);
    try {
      const session = await api.startSession({ topic_id: topicId, tag });
      navigate(`/sessions/${session.id}`);
    } catch (e) {
      setError(e instanceof ApiError ? e.message : "Could not start a session");
      setStarting(false);
    }
  }

  async function remove() {
    if (!topic) return;
    if (!confirm(`Delete “${topic.subject}” and everything you have answered in it?`)) return;
    try {
      await api.deleteTopic(topicId);
      navigate("/");
    } catch (e) {
      setError(e instanceof Error ? e.message : "Could not delete this topic");
    }
  }

  async function regenerate() {
    if (!topic) return;
    // Only warn when there is something to lose. A deck that never generated
    // has no questions and no history, so retrying it costs nothing.
    if (topic.fact_count > 0) {
      const answered = topic.fact_count - topic.new_count;
      const loses =
        answered > 0
          ? `This writes ${topic.fact_count} new questions and discards your progress on ${answered} of the old ones.`
          : `This replaces all ${topic.fact_count} questions.`;
      if (!confirm(`Rebuild “${topic.subject}”?\n\n${loses}`)) return;
    }

    setRegenerating(true);
    setError("");
    try {
      setTopic({ ...topic, ...(await api.regenerateTopic(topicId)) });
    } catch (e) {
      setError(e instanceof Error ? e.message : "Could not restart generation");
    } finally {
      setRegenerating(false);
    }
  }

  if (error && !topic) {
    return (
      <main className="narrow">
        <Banner>{error}</Banner>
        <Link className="btn ghost" to="/">
          Back to topics
        </Link>
      </main>
    );
  }

  if (!topic) {
    return (
      <main>
        <p className="muted">Loading…</p>
      </main>
    );
  }

  return (
    <main>
      <p className="eyebrow">Topic</p>
      <h1>{topic.subject}</h1>
      {topic.guidance && <p className="lede">{topic.guidance}</p>}

      {error && <Banner>{error}</Banner>}

      {topic.status === "generating" && (
        <div className="panel">
          <div className="row">
            <Spinner />
            <b>{topic.stage || "Working"}</b>
            <span className="muted">
              {topic.fact_count} of {topic.target_count} questions written
            </span>
          </div>
          <div className="progress-track">
            <div className="progress-fill" style={{ width: `${topic.progress}%` }} />
          </div>
          <p className="muted" style={{ margin: 0, fontSize: "0.8125rem" }}>
            Researching the subject and writing questions. You can leave this page.
          </p>
        </div>
      )}

      {topic.status === "failed" && (
        <>
          <Banner>Generation failed: {topic.error || "unknown error"}</Banner>
          <div className="row" style={{ marginBottom: "2.5rem" }}>
            <button className="btn" onClick={() => void regenerate()} disabled={regenerating}>
              {regenerating ? "Starting…" : "Try again"}
            </button>
            <button className="btn danger small" onClick={() => void remove()}>
              Delete topic
            </button>
            <span className="muted" style={{ fontSize: "0.8125rem" }}>
              Keeps the subject and settings — worth retrying once the cause is fixed.
            </span>
          </div>
        </>
      )}

      {topic.status === "ready" && (
        <>
          <div className="stat-grid">
            <div className="stat">
              <span className="value">{topic.fact_count}</span>
              <span className="label">Questions</span>
            </div>
            <div className="stat accent">
              <span className="value">{percent(topic.mastery)}</span>
              <span className="label">In memory</span>
            </div>
            <div className="stat">
              <span className="value">{topic.due_count}</span>
              <span className="label">Due now</span>
            </div>
            <div className="stat">
              <span className="value">{topic.new_count}</span>
              <span className="label">Never seen</span>
            </div>
          </div>

          <div className="row" style={{ marginBottom: "2.5rem" }}>
            <button className="btn" onClick={() => void start()} disabled={starting}>
              {starting ? "Starting…" : "Start a session"}
            </button>
            <button
              className="btn ghost small"
              onClick={() => void regenerate()}
              disabled={regenerating}
              title="Write a fresh set of questions for this subject"
            >
              {regenerating ? "Starting…" : "Rebuild deck"}
            </button>
            <button className="btn danger small" onClick={() => void remove()}>
              Delete topic
            </button>
          </div>

          <div className="panels">
            <div className="panel">
              <h2>Subtopics</h2>
              {topic.subtopics && topic.subtopics.length > 0 ? (
                topic.subtopics.map((s) => (
                  <div className="bar-row" key={s.tag}>
                    <span className="tag-name">{s.tag}</span>
                    <div className="bar-track">
                      <div
                        className="bar-fill"
                        style={{
                          width: `${(s.count / (topic.subtopics?.[0]?.count ?? 1)) * 100}%`,
                        }}
                      />
                    </div>
                    <span className="bar-value">{s.count}</span>
                  </div>
                ))
              ) : (
                <p className="muted">No subtopic tags on this deck.</p>
              )}
            </div>

            <div className="panel">
              <h2>How it is asked</h2>
              <div className="bar-row">
                <span className="tag-name">Recall</span>
                <div className="bar-track">
                  <div
                    className="bar-fill"
                    style={{ width: `${pctOf(topic.modes.open, topic.fact_count)}%` }}
                  />
                </div>
                <span className="bar-value">{topic.modes.open ?? 0}</span>
              </div>
              <div className="bar-row">
                <span className="tag-name">Multiple choice</span>
                <div className="bar-track">
                  <div
                    className="bar-fill"
                    style={{ width: `${pctOf(topic.modes.mc, topic.fact_count)}%` }}
                  />
                </div>
                <span className="bar-value">{topic.modes.mc ?? 0}</span>
              </div>
              <p className="muted" style={{ fontSize: "0.8125rem", marginBottom: 0 }}>
                Shaky facts are asked open-ended, because recalling something is
                harder — and sticks better — than recognising it. Multiple choice
                is what a fact graduates to.
              </p>
            </div>
          </div>

          <div className="panel">
            <h2>Every question</h2>
            {topic.facts.length === 0 ? (
              <Empty title="This deck is empty" />
            ) : (
              topic.facts.map((f) => <FactRow key={f.id} fact={f} />)
            )}
          </div>
        </>
      )}
    </main>
  );
}

function pctOf(n: number | undefined, total: number): number {
  if (!total) return 0;
  return Math.round(((n ?? 0) / total) * 100);
}

function FactRow({ fact }: { fact: Fact }) {
  const review = fact.review;
  // Predicted recall right now, from the same curve the scheduler uses.
  const recall = review
    ? Math.pow(
        1 +
          (19 / 81) *
            (Math.max(0, Date.now() - new Date(review.last_reviewed_at).getTime()) /
              86_400_000 /
              Math.max(review.stability, 0.01)),
        -0.5,
      )
    : 0;

  return (
    <div className="fact-row">
      <div>
        <div className="statement">{fact.statement}</div>
        <div className="answer">{fact.canonical_answer}</div>
        {fact.tags.length > 0 && (
          <div className="tag-list" style={{ marginTop: "0.35rem" }}>
            {fact.tags.map((t) => (
              <span className="pill" key={t}>
                {t}
              </span>
            ))}
          </div>
        )}
      </div>
      <div style={{ display: "flex", alignItems: "center", gap: "0.6rem" }}>
        {review ? (
          <>
            <DecayCurve
              stability={review.stability}
              days={curveWindow(review.stability)}
              width={54}
              height={18}
              className="muted"
            />
            <span className="bar-value" title={`${percent(recall)} predicted recall`}>
              {relativeDay(review.due_at)}
            </span>
          </>
        ) : (
          <span className="bar-value">not seen</span>
        )}
        <span className="recall-dot" style={{ background: recallColor(recall) }} />
      </div>
    </div>
  );
}
