import { useLayoutEffect, useMemo, useRef, useState } from "react";

import type { Interval, Parent, Sprint } from "../types";
import { stateColor, stateLabel } from "../types";

const LABEL_WIDTH = 268;
const ROW_HEIGHT = 26;
const BAR_HEIGHT = 14;
const GROUP_HEADER_HEIGHT = 30;
const AXIS_HEIGHT = 28;
const PADDING_RIGHT = 16;
const MIN_PLOT_WIDTH = 520;

/**
 * A 2px gap between adjacent segments, so a state change reads as a change
 * rather than as one continuous bar with a colour shift.
 */
const SEGMENT_GAP = 2;

interface Row {
  kind: "group" | "subtask";
  key: string;
  label: string;
  sublabel?: string;
  inScope?: boolean;
  intervals?: Interval[];
  y: number;
}

interface HoverState {
  x: number;
  y: number;
  subTask: string;
  interval: Interval;
}

function useContainerWidth() {
  const ref = useRef<HTMLDivElement>(null);
  const [width, setWidth] = useState(0);

  useLayoutEffect(() => {
    const element = ref.current;
    if (!element) return;
    const observer = new ResizeObserver((entries) => {
      setWidth(entries[0].contentRect.width);
    });
    observer.observe(element);
    setWidth(element.getBoundingClientRect().width);
    return () => observer.disconnect();
  }, []);

  return { ref, width };
}

function layout(parents: Parent[]): { rows: Row[]; height: number } {
  const rows: Row[] = [];
  let y = 0;

  for (const parent of parents) {
    rows.push({
      kind: "group",
      key: parent.key,
      label: `${parent.key} — ${parent.summary}`,
      inScope: parent.inScope,
      y,
    });
    y += GROUP_HEADER_HEIGHT;

    for (const subTask of parent.subtasks) {
      rows.push({
        kind: "subtask",
        key: subTask.key,
        label: subTask.key,
        sublabel: subTask.summary,
        intervals: subTask.intervals,
        y,
      });
      y += ROW_HEIGHT;
    }
  }
  return { rows, height: y };
}

/** Day boundaries inside the sprint, used for the recessive gridlines. */
function dayTicks(start: Date, end: Date): Date[] {
  const ticks: Date[] = [];
  const cursor = new Date(start);
  cursor.setHours(0, 0, 0, 0);
  cursor.setDate(cursor.getDate() + 1);
  while (cursor < end) {
    ticks.push(new Date(cursor));
    cursor.setDate(cursor.getDate() + 1);
  }
  return ticks;
}

function truncate(text: string, limit: number): string {
  return text.length > limit ? `${text.slice(0, limit - 1)}\u2026` : text;
}

function formatDay(date: Date): string {
  return date.toLocaleDateString(undefined, { month: "short", day: "numeric" });
}

function formatDuration(from: string, to: string): string {
  const hours = (new Date(to).getTime() - new Date(from).getTime()) / 3_600_000;
  if (hours < 1) return "under an hour";
  if (hours < 48) return `${Math.round(hours)}h`;
  return `${(hours / 24).toFixed(1)} days`;
}

export function Timeline({ parents, sprint }: { parents: Parent[]; sprint: Sprint }) {
  const { ref, width } = useContainerWidth();
  const [hover, setHover] = useState<HoverState | null>(null);

  const start = useMemo(() => new Date(sprint.start), [sprint.start]);
  const end = useMemo(() => new Date(sprint.end), [sprint.end]);

  const { rows, height } = useMemo(() => layout(parents), [parents]);

  const plotWidth = Math.max(MIN_PLOT_WIDTH, width - LABEL_WIDTH - PADDING_RIGHT);
  const totalWidth = LABEL_WIDTH + plotWidth + PADDING_RIGHT;
  const totalHeight = AXIS_HEIGHT + height + 8;

  const span = end.getTime() - start.getTime();
  const scale = (iso: string) => {
    const ratio = (new Date(iso).getTime() - start.getTime()) / span;
    return LABEL_WIDTH + Math.max(0, Math.min(1, ratio)) * plotWidth;
  };

  const ticks = useMemo(() => dayTicks(start, end), [start, end]);

  if (rows.length === 0) {
    return <p className="muted">No sub-tasks to chart for this sprint.</p>;
  }

  return (
    <div className="scroll-x" ref={ref} style={{ position: "relative" }}>
      <svg
        width={totalWidth}
        height={totalHeight}
        role="img"
        aria-label={`State timeline for ${sprint.name}, grouped by parent work item`}
        style={{ display: "block" }}
      >
        {/* Recessive gridlines: day boundaries, behind everything. */}
        <g>
          {ticks.map((tick) => {
            const x = scale(tick.toISOString());
            return (
              <g key={tick.toISOString()}>
                <line
                  x1={x}
                  x2={x}
                  y1={AXIS_HEIGHT - 6}
                  y2={totalHeight}
                  stroke="var(--gridline)"
                  strokeWidth={1}
                />
                <text x={x + 4} y={AXIS_HEIGHT - 12} fontSize={11} fill="var(--text-muted)">
                  {formatDay(tick)}
                </text>
              </g>
            );
          })}
          <line
            x1={LABEL_WIDTH}
            x2={LABEL_WIDTH + plotWidth}
            y1={AXIS_HEIGHT - 6}
            y2={AXIS_HEIGHT - 6}
            stroke="var(--gridline)"
            strokeWidth={1}
          />
        </g>

        {rows.map((row) => {
          const y = AXIS_HEIGHT + row.y;

          if (row.kind === "group") {
            return (
              <g key={`group-${row.key}`}>
                <text
                  x={0}
                  y={y + GROUP_HEADER_HEIGHT - 10}
                  fontSize={12}
                  fontWeight={600}
                  fill="var(--text-primary)"
                >
                  {truncate(row.label, 40)}
                </text>
                {row.inScope === false && (
                  <text
                    x={LABEL_WIDTH + plotWidth}
                    y={y + GROUP_HEADER_HEIGHT - 10}
                    fontSize={11}
                    textAnchor="end"
                    fill="var(--text-muted)"
                  >
                    not in committed scope
                  </text>
                )}
              </g>
            );
          }

          const barY = y + (ROW_HEIGHT - BAR_HEIGHT) / 2;
          const intervals = row.intervals ?? [];

          return (
            <g key={`row-${row.key}`}>
              <text x={12} y={barY + BAR_HEIGHT - 2} fontSize={12} fill="var(--text-secondary)">
                <title>{`${row.label} — ${row.sublabel ?? ""}`}</title>
                <tspan fill="var(--text-primary)">{row.label}</tspan>
                <tspan dx={6}>{truncate(row.sublabel ?? "", 26)}</tspan>
              </text>

              {intervals.map((interval, index) => {
                const x1 = scale(interval.from);
                const x2 = scale(interval.to);
                const isFirst = index === 0;
                const isLast = index === intervals.length - 1;
                const rawWidth = x2 - x1 - (isLast ? 0 : SEGMENT_GAP);
                const barWidth = Math.max(1, rawWidth);

                return (
                  <rect
                    key={`${interval.from}-${index}`}
                    x={x1}
                    y={barY}
                    width={barWidth}
                    height={BAR_HEIGHT}
                    rx={isFirst || isLast ? 4 : 1}
                    fill={stateColor(interval.state)}
                    onMouseEnter={(event) =>
                      setHover({
                        x: event.clientX,
                        y: event.clientY,
                        subTask: `${row.label} — ${row.sublabel ?? ""}`,
                        interval,
                      })
                    }
                    onMouseMove={(event) =>
                      setHover((current) =>
                        current ? { ...current, x: event.clientX, y: event.clientY } : current,
                      )
                    }
                    onMouseLeave={() => setHover(null)}
                    aria-label={`${row.label}: ${stateLabel(interval.state)} for ${formatDuration(
                      interval.from,
                      interval.to,
                    )}`}
                  />
                );
              })}
            </g>
          );
        })}
      </svg>

      {hover && (
        <div
          role="tooltip"
          style={{
            position: "fixed",
            left: hover.x + 14,
            top: hover.y + 14,
            zIndex: 10,
            pointerEvents: "none",
            background: "var(--surface)",
            border: "1px solid var(--border)",
            borderRadius: 8,
            padding: "8px 10px",
            fontSize: 12,
            maxWidth: 320,
            boxShadow: "0 6px 20px rgba(0,0,0,0.14)",
          }}
        >
          <div style={{ fontWeight: 600, marginBottom: 4 }}>{hover.subTask}</div>
          <div className="row" style={{ gap: 6 }}>
            <span
              className="swatch"
              style={{ background: stateColor(hover.interval.state) }}
              aria-hidden="true"
            />
            <span>{stateLabel(hover.interval.state)}</span>
          </div>
          <div className="muted" style={{ marginTop: 4 }}>
            {new Date(hover.interval.from).toLocaleString()} →{" "}
            {new Date(hover.interval.to).toLocaleString()}
          </div>
          <div className="muted">{formatDuration(hover.interval.from, hover.interval.to)}</div>
        </div>
      )}
    </div>
  );
}
