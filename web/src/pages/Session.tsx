import { useCallback, useEffect, useRef, useState } from "react";
import { Link, useParams } from "react-router-dom";

import {
  api,
  type AnswerResponse,
  type Grade,
  type QuestionView,
  type SessionResult,
} from "../api";
import {
  Banner,
  DecayCurve,
  Spinner,
  Tally,
  curveWindow,
  daysUntil,
  percent,
  relativeDay,
} from "../components";

export function SessionPage() {
  const { id } = useParams();
  const sessionId = Number(id);

  const [question, setQuestion] = useState<QuestionView | null>(null);
  const [feedback, setFeedback] = useState<AnswerResponse | null>(null);
  const [summary, setSummary] = useState<SessionResult | null>(null);
  const [results, setResults] = useState<Grade[]>([]);
  const [total, setTotal] = useState(0);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  const [response, setResponse] = useState("");
  const [choice, setChoice] = useState<number | null>(null);

  // When the question appeared, so the answer's latency can distinguish fluent
  // recall from laboured recall.
  const shownAt = useRef(Date.now());
  const inputRef = useRef<HTMLInputElement>(null);

  const advance = useCallback(async () => {
    setBusy(true);
    try {
      const next = await api.next(sessionId);
      setTotal(next.progress.total);
      if (next.done) {
        setQuestion(null);
        setSummary(next.summary ?? null);
      } else {
        setQuestion(next.question ?? null);
        setFeedback(null);
        setResponse("");
        setChoice(null);
        shownAt.current = Date.now();
      }
      setError("");
    } catch (e) {
      setError(e instanceof Error ? e.message : "Could not load the next question");
    } finally {
      setBusy(false);
    }
  }, [sessionId]);

  useEffect(() => {
    void advance();
  }, [advance]);

  // Focus the answer box on every new open-ended question so the whole session
  // can be done from the keyboard.
  useEffect(() => {
    if (question?.mode === "open" && !feedback) inputRef.current?.focus();
  }, [question, feedback]);

  const submit = useCallback(
    async (pickedChoice?: number) => {
      if (!question || feedback || busy) return;
      const picked = pickedChoice ?? choice;
      if (question.mode === "mc" && picked === null) return;
      if (question.mode === "open" && !response.trim()) return;

      setBusy(true);
      try {
        const result = await api.answer(sessionId, {
          question_id: question.id,
          response: question.mode === "open" ? response : undefined,
          choice: question.mode === "mc" ? picked : null,
          latency_ms: Date.now() - shownAt.current,
        });
        setFeedback(result);
        setResults((prev) => [...prev, result.grade]);
        setTotal(result.progress.total);
        setError("");
      } catch (e) {
        setError(e instanceof Error ? e.message : "Could not submit that answer");
      } finally {
        setBusy(false);
      }
    },
    [question, feedback, busy, choice, response, sessionId],
  );

  // Keyboard: 1–4 pick an option, Enter submits, Enter again moves on.
  useEffect(() => {
    function onKey(e: KeyboardEvent) {
      if (e.metaKey || e.ctrlKey || e.altKey) return;

      if (feedback) {
        if (e.key === "Enter") {
          e.preventDefault();
          void advance();
        }
        return;
      }
      if (!question) return;

      if (question.mode === "mc") {
        const n = Number(e.key);
        if (n >= 1 && n <= (question.options?.length ?? 0)) {
          e.preventDefault();
          setChoice(n - 1);
          void submit(n - 1);
        }
      }
    }
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [question, feedback, advance, submit]);

  if (summary) return <SessionSummary summary={summary} results={results} />;

  return (
    <main className="narrow">
      <div className="session-head">
        <Tally results={results} total={total} current={results.length} />
        <span className="count num">
          {results.length} / {total}
        </span>
      </div>

      {error && <Banner>{error}</Banner>}

      {!question && !error && (
        <p className="muted">
          <Spinner /> Loading…
        </p>
      )}

      {question && (
        <>
          <p className="eyebrow">
            {question.subject}
            {question.tags?.length ? ` · ${question.tags.join(" · ")}` : ""}
            {question.is_repeat && " · asked again"}
          </p>

          <h2 className="question">{question.prompt}</h2>

          {question.mode === "mc" ? (
            <div className="options">
              {question.options?.map((opt, i) => (
                <button
                  key={i}
                  className={optionClass(i, choice, feedback)}
                  disabled={!!feedback || busy}
                  onClick={() => {
                    setChoice(i);
                    void submit(i);
                  }}
                >
                  <span className="key">{i + 1}</span>
                  <span>{opt}</span>
                </button>
              ))}
            </div>
          ) : (
            <form
              className="answer-form"
              onSubmit={(e) => {
                e.preventDefault();
                void submit();
              }}
            >
              <input
                ref={inputRef}
                className="input"
                value={response}
                onChange={(e) => setResponse(e.target.value)}
                placeholder="Answer from memory"
                disabled={!!feedback || busy}
                autoComplete="off"
                spellCheck={false}
              />
              {!feedback && (
                <button className="btn" type="submit" disabled={busy || !response.trim()}>
                  {busy ? "Checking…" : "Answer"}
                  <span className="kbd">↵</span>
                </button>
              )}
            </form>
          )}

          {!feedback && (
            <p className="mode-note">
              {question.mode === "open"
                ? "Recall — type what you remember"
                : "Recognise — press 1 to 4"}
            </p>
          )}

          {feedback && <Feedback feedback={feedback} onNext={() => void advance()} busy={busy} />}
        </>
      )}
    </main>
  );
}

function optionClass(index: number, choice: number | null, feedback: AnswerResponse | null): string {
  const base = "option";
  if (!feedback) return choice === index ? `${base} chosen` : base;
  if (index === feedback.correct_option) return `${base} right`;
  if (index === choice) return `${base} wrong`;
  return base;
}

function Feedback({
  feedback,
  onNext,
  busy,
}: {
  feedback: AnswerResponse;
  onNext: () => void;
  busy: boolean;
}) {
  const [elaboration, setElaboration] = useState("");
  const [loading, setLoading] = useState(false);
  const [failed, setFailed] = useState("");
  const nextRef = useRef<HTMLButtonElement>(null);

  useEffect(() => nextRef.current?.focus(), []);

  async function explain() {
    setLoading(true);
    setFailed("");
    try {
      const res = await api.explain(feedback.attempt_id);
      setElaboration(res.elaboration);
    } catch (e) {
      setFailed(e instanceof Error ? e.message : "Could not fetch an explanation");
    } finally {
      setLoading(false);
    }
  }

  const days = daysUntil(feedback.next_due);

  return (
    <div className={`feedback ${feedback.grade}`}>
      <div className="verdict">
        {feedback.grade === "correct"
          ? "Correct"
          : feedback.grade === "partial"
            ? "Partly right"
            : "Not right"}
      </div>

      <p className="canonical">{feedback.canonical_answer}</p>
      {feedback.critique && <p className="critique">{feedback.critique}</p>}
      {feedback.explanation && <p className="explanation">{feedback.explanation}</p>}

      {elaboration && <div className="elaboration">{elaboration}</div>}
      {failed && <p className="critique">{failed}</p>}

      <div className="feedback-foot">
        <span className="schedule">
          <DecayCurve
            stability={Math.max(days, 0.02)}
            days={curveWindow(days)}
            markAt={days}
            width={48}
            height={16}
          />
          Back {relativeDay(feedback.next_due)}
          {feedback.will_repeat && " · and again before you finish"}
        </span>

        {!elaboration && (
          <button className="btn ghost small" onClick={() => void explain()} disabled={loading}>
            {loading ? "Thinking…" : "Explain more"}
          </button>
        )}

        <button ref={nextRef} className="btn" onClick={onNext} disabled={busy}>
          Next
          <span className="kbd">↵</span>
        </button>
      </div>
    </div>
  );
}

function SessionSummary({
  summary,
  results,
}: {
  summary: SessionResult;
  results: Grade[];
}) {
  const accuracy = summary.answered > 0 ? summary.correct / summary.answered : 0;
  const topicId = summary.session.topic_id;

  return (
    <main className="narrow">
      <p className="eyebrow">Session complete</p>
      <h1>{summary.answered} answered</h1>

      {/* Only meaningful when this page was reached by finishing the session;
          on a reload there is no per-answer record to draw. */}
      {results.length > 0 && (
        <div style={{ margin: "0 0 2rem" }}>
          <Tally results={results} total={results.length} current={-1} />
        </div>
      )}

      <div className="stat-grid">
        <div className="stat accent">
          <span className="value">{percent(accuracy)}</span>
          <span className="label">Correct</span>
        </div>
        <div className="stat">
          <span className="value">{summary.correct}</span>
          <span className="label">Right</span>
        </div>
        <div className="stat">
          <span className="value">{summary.partial}</span>
          <span className="label">Partly</span>
        </div>
        <div className="stat">
          <span className="value">{summary.incorrect}</span>
          <span className="label">Wrong</span>
        </div>
      </div>

      {summary.weak_tags && summary.weak_tags.length > 0 && (
        <div className="panel">
          <h2>Where you struggled</h2>
          {summary.weak_tags.map((t) => (
            <div className="bar-row" key={t.tag}>
              <span className="tag-name">{t.tag}</span>
              <div className="bar-track">
                <div
                  className={`bar-fill ${t.accuracy < 0.6 ? "weak" : ""}`}
                  style={{ width: `${t.accuracy * 100}%` }}
                />
              </div>
              <span className="bar-value">
                {t.correct}/{t.attempts}
              </span>
            </div>
          ))}
        </div>
      )}

      {summary.upcoming && summary.upcoming.length > 0 && (
        <div className="panel">
          <h2>Coming back</h2>
          {summary.upcoming.slice(0, 8).map((u) => (
            <div className="bar-row" key={u.fact_id} style={{ gridTemplateColumns: "1fr auto" }}>
              <span className="tag-name" title={u.statement}>
                {u.statement}
              </span>
              <span className="bar-value">{relativeDay(u.due_at)}</span>
            </div>
          ))}
        </div>
      )}

      <div className="row" style={{ marginTop: "1.5rem" }}>
        {topicId ? (
          <Link className="btn" to={`/topics/${topicId}`}>
            Back to the topic
          </Link>
        ) : (
          <Link className="btn" to="/">
            Back to topics
          </Link>
        )}
        <Link className="btn ghost" to="/progress">
          See your progress
        </Link>
      </div>
    </main>
  );
}
