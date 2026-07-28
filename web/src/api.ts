// Typed client for the Trivial API (SPEC.md §9).

export type TopicStatus = "generating" | "ready" | "failed";
export type Mode = "open" | "mc";
export type Grade = "correct" | "partial" | "incorrect";
export type SessionKind = "topic" | "review" | "drill";

export interface Review {
  fact_id: number;
  stability: number;
  difficulty: number;
  due_at: string;
  last_reviewed_at: string;
  reps: number;
  lapses: number;
}

export interface Question {
  id: number;
  fact_id: number;
  mode: Mode;
  prompt: string;
  options?: string[];
}

export interface Fact {
  id: number;
  topic_id: number;
  statement: string;
  canonical_answer: string;
  explanation: string;
  tags: string[];
  difficulty: number;
  questions?: Question[];
  review?: Review;
}

export interface TopicSummary {
  id: number;
  subject: string;
  guidance?: string;
  status: TopicStatus;
  target_count: number;
  stage?: string;
  progress: number;
  error?: string;
  created_at: string;
  updated_at: string;
  fact_count: number;
  due_count: number;
  new_count: number;
  /** Mean predicted recall across the deck, 0–1. */
  mastery: number;
  /** Mean FSRS stability in days across seen facts. */
  stability: number;
}

export interface TopicDetail extends TopicSummary {
  facts: Fact[];
  modes: Partial<Record<Mode, number>>;
  subtopics: { tag: string; count: number }[] | null;
}

export interface GenerationStatus {
  topic_id: number;
  status: TopicStatus;
  stage: string;
  progress: number;
  error?: string;
  fact_count: number;
  target_count: number;
}

export interface Session {
  id: number;
  topic_id?: number;
  kind: SessionKind;
  tag?: string;
  planned: number;
  started_at: string;
  finished_at?: string;
}

export interface QuestionView {
  id: number;
  fact_id: number;
  mode: Mode;
  prompt: string;
  options?: string[];
  subject: string;
  tags: string[] | null;
  is_repeat: boolean;
}

export interface Progress {
  answered: number;
  total: number;
}

export interface TagScore {
  tag: string;
  attempts: number;
  correct: number;
  accuracy: number;
}

export interface ScheduledFact {
  fact_id: number;
  statement: string;
  due_at: string;
  stability: number;
}

export interface SessionResult {
  session: Session;
  answered: number;
  correct: number;
  partial: number;
  incorrect: number;
  weak_tags: TagScore[] | null;
  upcoming: ScheduledFact[] | null;
}

export interface NextResponse {
  done: boolean;
  question?: QuestionView;
  progress: Progress;
  summary?: SessionResult;
}

export interface AnswerResponse {
  attempt_id: number;
  grade: Grade;
  critique: string;
  canonical_answer: string;
  explanation: string;
  correct_option: number;
  next_due: string;
  will_repeat: boolean;
  progress: Progress;
}

export interface Stats {
  totals: {
    topics: number;
    facts: number;
    seen: number;
    due_now: number;
    new_facts: number;
    mastery: number;
    attempts: number;
    accuracy: number;
    streak_days: number;
  };
  history: { date: string; attempts: number; correct: number; accuracy: number }[] | null;
  weak_tags: TagScore[] | null;
  due_queue: { date: string; count: number; overdue?: boolean }[] | null;
  topics: { topic_id: number; subject: string; facts: number; due: number; mastery: number }[] | null;
}

/** ApiError carries the server's message so the UI can show it verbatim. */
export class ApiError extends Error {
  constructor(
    readonly status: number,
    message: string,
  ) {
    super(message);
    this.name = "ApiError";
  }
}

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const res = await fetch(`/api/v1${path}`, {
    method,
    headers: body === undefined ? undefined : { "Content-Type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
  });

  if (res.status === 204) return undefined as T;

  const text = await res.text();
  const parsed: unknown = text ? JSON.parse(text) : null;

  if (!res.ok) {
    const message =
      parsed && typeof parsed === "object" && "error" in parsed
        ? String((parsed as { error: unknown }).error)
        : `Request failed (${res.status})`;
    throw new ApiError(res.status, message);
  }
  return parsed as T;
}

export const api = {
  listTopics: () => request<TopicSummary[]>("GET", "/topics"),

  createTopic: (subject: string, guidance: string, targetCount?: number) =>
    request<TopicSummary>("POST", "/topics", {
      subject,
      guidance,
      target_count: targetCount ?? 0,
    }),

  getTopic: (id: number) => request<TopicDetail>("GET", `/topics/${id}`),

  deleteTopic: (id: number) => request<void>("DELETE", `/topics/${id}`),

  regenerateTopic: (id: number) => request<TopicSummary>("POST", `/topics/${id}/regenerate`),

  generationStatus: (id: number) => request<GenerationStatus>("GET", `/topics/${id}/generation`),

  startSession: (opts: { topic_id?: number; tag?: string; size?: number }) =>
    request<Session>("POST", "/sessions", {
      topic_id: opts.topic_id ?? null,
      tag: opts.tag ?? "",
      size: opts.size ?? 0,
    }),

  next: (sessionId: number) => request<NextResponse>("GET", `/sessions/${sessionId}/next`),

  answer: (
    sessionId: number,
    body: { question_id: number; response?: string; choice?: number | null; latency_ms: number },
  ) =>
    request<AnswerResponse>("POST", `/sessions/${sessionId}/answer`, {
      question_id: body.question_id,
      response: body.response ?? "",
      choice: body.choice ?? null,
      latency_ms: body.latency_ms,
    }),

  explain: (attemptId: number) =>
    request<{ elaboration: string }>("POST", `/attempts/${attemptId}/explain`),

  stats: () => request<Stats>("GET", "/stats"),
};
