import { useEffect, useState } from "react";
import { Link, useNavigate } from "react-router-dom";

import { ApiError, api, type Stats } from "../api";
import { Banner, Empty, percent } from "../components";

export function Dashboard() {
  const [stats, setStats] = useState<Stats | null>(null);
  const [error, setError] = useState("");
  const navigate = useNavigate();

  useEffect(() => {
    api
      .stats()
      .then(setStats)
      .catch((e: unknown) =>
        setError(e instanceof Error ? e.message : "Could not load your progress"),
      );
  }, []);

  async function drill(tag: string) {
    try {
      const session = await api.startSession({ tag });
      navigate(`/sessions/${session.id}`);
    } catch (e) {
      setError(e instanceof ApiError ? e.message : "Could not start a drill");
    }
  }

  async function reviewDue() {
    try {
      const session = await api.startSession({});
      navigate(`/sessions/${session.id}`);
    } catch (e) {
      setError(e instanceof ApiError ? e.message : "Could not start a review");
    }
  }

  if (error && !stats) {
    return (
      <main>
        <Banner>{error}</Banner>
      </main>
    );
  }
  if (!stats) {
    return (
      <main>
        <p className="muted">Loading…</p>
      </main>
    );
  }

  const t = stats.totals;
  if (t.facts === 0) {
    return (
      <main>
        <p className="eyebrow">Progress</p>
        <h1>Nothing measured yet</h1>
        <Empty title="No questions to track">
          <p>Build a topic first, then this page fills in as you answer.</p>
          <Link className="btn" to="/" style={{ marginTop: "1rem" }}>
            Go to topics
          </Link>
        </Empty>
      </main>
    );
  }

  const history = stats.history ?? [];
  const peak = Math.max(1, ...history.map((d) => d.attempts));
  const dueQueue = stats.due_queue ?? [];
  const duePeak = Math.max(1, ...dueQueue.map((d) => d.count));

  return (
    <main>
      <p className="eyebrow">Progress</p>
      <h1>What you know</h1>
      <p className="lede">
        Mastery is your predicted recall right now, averaged over every question
        in every deck. It falls between sessions — that is the point.
      </p>

      {error && <Banner>{error}</Banner>}

      <div className="stat-grid">
        <div className="stat accent">
          <span className="value">{percent(t.mastery)}</span>
          <span className="label">In memory</span>
        </div>
        <div className="stat">
          <span className="value">{t.due_now}</span>
          <span className="label">Due now</span>
        </div>
        <div className="stat">
          <span className="value">{t.facts}</span>
          <span className="label">Questions</span>
        </div>
        <div className="stat">
          <span className="value">{percent(t.accuracy)}</span>
          <span className="label">Lifetime accuracy</span>
        </div>
        <div className="stat">
          <span className="value">{t.streak_days}</span>
          <span className="label">Day streak</span>
        </div>
      </div>

      {t.due_now > 0 && (
        <div className="row" style={{ marginBottom: "2.5rem" }}>
          <button className="btn" onClick={() => void reviewDue()}>
            Review {t.due_now} due now
          </button>
        </div>
      )}

      <div className="panels">
        <div className="panel">
          <h2>Weakest subtopics</h2>
          {stats.weak_tags && stats.weak_tags.length > 0 ? (
            <>
              {stats.weak_tags.map((tag) => (
                <div className="bar-row" key={tag.tag}>
                  <button
                    className="tag-name"
                    onClick={() => void drill(tag.tag)}
                    title={`Drill ${tag.tag}`}
                    style={{
                      background: "none",
                      border: 0,
                      padding: 0,
                      textAlign: "left",
                      cursor: "pointer",
                      textDecoration: "underline",
                      textDecorationColor: "var(--rule)",
                      textUnderlineOffset: "3px",
                    }}
                  >
                    {tag.tag}
                  </button>
                  <div className="bar-track">
                    <div
                      className={`bar-fill ${tag.accuracy < 0.6 ? "weak" : ""}`}
                      style={{ width: `${tag.accuracy * 100}%` }}
                    />
                  </div>
                  <span className="bar-value">{percent(tag.accuracy)}</span>
                </div>
              ))}
              <p className="muted" style={{ fontSize: "0.8125rem", margin: "0.75rem 0 0" }}>
                Pick one to drill just those questions.
              </p>
            </>
          ) : (
            <p className="muted">
              Answer a few more questions and your weak spots will show up here.
            </p>
          )}
        </div>

        <div className="panel">
          <h2>Coming due</h2>
          <div className="due-chart">
            {dueQueue.map((b, i) => (
              <div
                key={`${b.date}-${i}`}
                className={`due-bar ${b.overdue ? "overdue" : ""}`}
                style={{ height: `${(b.count / duePeak) * 100}%` }}
                title={`${b.count} ${b.overdue ? "overdue" : `due ${b.date}`}`}
              />
            ))}
          </div>
          <div className="due-axis">
            <span>overdue</span>
            <span>two weeks out</span>
          </div>
        </div>

        <div className="panel">
          <h2>Last 30 days</h2>
          <div className="due-chart">
            {history.map((d) => (
              <div
                key={d.date}
                className="due-bar"
                style={{
                  height: `${(d.attempts / peak) * 100}%`,
                  opacity: d.attempts ? 0.4 + d.accuracy * 0.6 : 0.15,
                }}
                title={`${d.date}: ${d.attempts} answered, ${percent(d.accuracy)} correct`}
              />
            ))}
          </div>
          <div className="due-axis">
            <span>30 days ago</span>
            <span>{t.attempts} answers total</span>
          </div>
        </div>

        <div className="panel">
          <h2>By topic</h2>
          {(stats.topics ?? []).map((topic) => (
            <div className="bar-row" key={topic.topic_id}>
              <Link className="tag-name" to={`/topics/${topic.topic_id}`}>
                {topic.subject}
              </Link>
              <div className="bar-track">
                <div className="bar-fill" style={{ width: `${topic.mastery * 100}%` }} />
              </div>
              <span className="bar-value">{percent(topic.mastery)}</span>
            </div>
          ))}
        </div>
      </div>
    </main>
  );
}
