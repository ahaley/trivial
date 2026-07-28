import type { ReactNode } from "react";

/**
 * DecayCurve draws the forgetting curve R(t) = (1 + 19/81 · t/S)^-0.5 — the
 * same curve the scheduler plans against. It is the app's signature element:
 * every deck, every fact and every schedule is really a picture of this shape,
 * so the interface draws it rather than describing it.
 */
export function DecayCurve({
  stability,
  days,
  markAt,
  className,
  width = 120,
  height = 40,
}: {
  /** Stability in days; larger means a flatter curve. */
  stability: number;
  /** How many days of the curve to draw. */
  days: number;
  /** Optional day at which to mark the next review. */
  markAt?: number;
  className?: string;
  width?: number;
  height?: number;
}) {
  const s = Math.max(stability, 0.01);
  const span = Math.max(days, 0.01);
  const steps = 48;

  const r = (t: number) => Math.pow(1 + (19 / 81) * (t / s), -0.5);
  const points: string[] = [];
  for (let i = 0; i <= steps; i++) {
    const t = (i / steps) * span;
    const x = (i / steps) * width;
    const y = height - r(t) * height;
    points.push(`${x.toFixed(2)},${y.toFixed(2)}`);
  }

  const markX = markAt === undefined ? null : Math.min(markAt / span, 1) * width;
  const markY = markAt === undefined ? null : height - r(markAt) * height;

  return (
    <svg
      className={className}
      // Intrinsic size as well as viewBox: inline uses have no box of their
      // own to fill, and would otherwise collapse to the SVG default.
      width={width}
      height={height}
      viewBox={`0 0 ${width} ${height}`}
      preserveAspectRatio="none"
      aria-hidden="true"
      focusable="false"
    >
      <polyline
        points={points.join(" ")}
        fill="none"
        stroke="currentColor"
        strokeWidth="1.5"
        vectorEffect="non-scaling-stroke"
        strokeLinecap="round"
      />
      {markX !== null && markY !== null && (
        <circle cx={markX} cy={markY} r="2.5" fill="currentColor" />
      )}
    </svg>
  );
}

/** Trivium: the crossing of three roads, which is where the word came from. */
export function BrandMark() {
  return (
    <svg
      className="brand-mark"
      width="18"
      height="18"
      viewBox="0 0 18 18"
      fill="none"
      aria-hidden="true"
    >
      <path
        d="M9 17V9M9 9L1.5 4.5M9 9L16.5 4.5"
        stroke="currentColor"
        strokeWidth="1.75"
        strokeLinecap="round"
      />
      <circle cx="9" cy="9" r="2" fill="currentColor" />
    </svg>
  );
}

/** Tally renders one tick per question, coloured by how that answer went. */
export function Tally({
  results,
  total,
  current,
}: {
  results: ("correct" | "partial" | "incorrect")[];
  total: number;
  current: number;
}) {
  const ticks = Math.max(total, results.length);
  return (
    <div className="tally" role="img" aria-label={`${results.length} of ${ticks} answered`}>
      {Array.from({ length: ticks }, (_, i) => {
        const grade = results[i];
        const cls = grade ?? (i === current ? "current" : "");
        return <span key={i} className={`tick ${cls}`} />;
      })}
    </div>
  );
}

/** Percent formats a 0–1 ratio for display. */
export function percent(v: number): string {
  return `${Math.round(v * 100)}%`;
}

/**
 * relativeDay turns a due date into the phrasing a person would use. The unit
 * matters here: "in 4 minutes" and "in 4 months" are the two ends of what the
 * scheduler produces, and both need to read naturally.
 */
export function relativeDay(iso: string, now = Date.now()): string {
  const ms = new Date(iso).getTime() - now;
  if (Number.isNaN(ms)) return "";
  if (ms <= 0) return "now";

  const minutes = Math.round(ms / 60000);
  if (minutes < 60) return `in ${minutes} min`;

  const hours = Math.round(minutes / 60);
  if (hours < 24) return `in ${hours} hour${hours === 1 ? "" : "s"}`;

  const days = Math.round(hours / 24);
  if (days < 31) return `in ${days} day${days === 1 ? "" : "s"}`;

  const months = Math.round(days / 30.44);
  if (months < 24) return `in ${months} month${months === 1 ? "" : "s"}`;

  return `in ${Math.round(days / 365)} years`;
}

/** daysUntil is the fractional days between now and an ISO timestamp. */
export function daysUntil(iso: string, now = Date.now()): number {
  return Math.max((new Date(iso).getTime() - now) / 86_400_000, 0);
}

/**
 * curveWindow picks how many days of a decay curve to draw.
 *
 * Scaling the window to the fact's own stability would draw the identical
 * shape every time — decorative, not informative. Anchoring it to a fixed
 * month instead means a fact due in five minutes falls off a cliff and a fact
 * due in a season barely slopes, which is the actual difference between them.
 */
export function curveWindow(stability: number): number {
  return Math.max(30, stability * 1.5);
}

export function Banner({ children }: { children: ReactNode }) {
  return (
    <div className="banner" role="alert">
      {children}
    </div>
  );
}

export function Empty({
  title,
  children,
}: {
  title: string;
  children?: ReactNode;
}) {
  return (
    <div className="empty">
      <h2>{title}</h2>
      {children}
    </div>
  );
}

export function Spinner() {
  return <span className="spinner" aria-hidden="true" />;
}

/** recallColor maps predicted recall onto the grade palette. */
export function recallColor(recall: number): string {
  if (recall >= 0.9) return "var(--correct)";
  if (recall >= 0.7) return "var(--partial)";
  if (recall > 0) return "var(--incorrect)";
  return "var(--rule)";
}
