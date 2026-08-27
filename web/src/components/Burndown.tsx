import { useLayoutEffect, useMemo, useRef, useState } from "react";

import { buildCompressedScale, buildLinearScale } from "../timescale";
import type { AxisSegment, Burndown as BurndownData, BurndownPoint, Sprint } from "../types";

/**
 * The burndown shares the timeline's left gutter and x-scale, so the two charts
 * stack into one reading: a drop in the points line sits directly under the
 * sub-task row whose merge caused it.
 */
const LABEL_WIDTH = 268;
const PADDING_RIGHT = 16;
const MIN_PLOT_WIDTH = 520;
const PLOT_HEIGHT = 168;
const AXIS_HEIGHT = 26;
const TOP_PADDING = 12;

function useContainerWidth() {
  const ref = useRef<HTMLDivElement>(null);
  const [width, setWidth] = useState(0);

  useLayoutEffect(() => {
    const element = ref.current;
    if (!element) return;
    const observer = new ResizeObserver((entries) => setWidth(entries[0].contentRect.width));
    observer.observe(element);
    setWidth(element.getBoundingClientRect().width);
    return () => observer.disconnect();
  }, []);

  return { ref, width };
}

/**
 * Ticks at whole points while the totals are small, then in steps that keep the
 * axis to a handful of lines. A gridline every point on a 40-point sprint is
 * noise standing between the reader and the two lines that matter.
 */
function pointTicks(total: number): number[] {
  if (total <= 0) return [0];
  const target = 4;
  const raw = total / target;
  const magnitude = Math.pow(10, Math.floor(Math.log10(raw)));
  const step = [1, 2, 5, 10].map((m) => m * magnitude).find((s) => s >= raw) ?? magnitude * 10;

  const ticks: number[] = [];
  for (let value = 0; value <= total + step / 2; value += step) ticks.push(value);
  return ticks;
}

function path(points: BurndownPoint[], x: (iso: string) => number, y: (value: number) => number): string {
  if (points.length === 0) return "";
  return points.map((p, i) => `${i === 0 ? "M" : "L"}${x(p.at).toFixed(2)},${y(p.remaining).toFixed(2)}`).join(" ");
}

function remainingAt(points: BurndownPoint[], ms: number): number | null {
  let value: number | null = null;
  for (const point of points) {
    if (new Date(point.at).getTime() > ms) break;
    value = point.remaining;
  }
  return value;
}

function formatPoints(value: number): string {
  return Number.isInteger(value) ? String(value) : value.toFixed(1);
}

interface HoverState {
  x: number;
  ms: number;
  remaining: number | null;
  ideal: number | null;
}

export function Burndown({
  burndown,
  sprint,
  axis,
  compressed,
}: {
  burndown: BurndownData;
  sprint: Sprint;
  axis: AxisSegment[];
  compressed: boolean;
}) {
  const { ref, width } = useContainerWidth();
  const [hover, setHover] = useState<HoverState | null>(null);

  const start = useMemo(() => new Date(sprint.start), [sprint.start]);
  const end = useMemo(() => new Date(sprint.end), [sprint.end]);

  const plotWidth = Math.max(MIN_PLOT_WIDTH, width - LABEL_WIDTH - PADDING_RIGHT);
  const totalWidth = LABEL_WIDTH + plotWidth + PADDING_RIGHT;
  const height = TOP_PADDING + PLOT_HEIGHT + AXIS_HEIGHT;

  const scale = useMemo(() => {
    const linear = buildLinearScale(start.getTime(), end.getTime(), LABEL_WIDTH, plotWidth);
    if (!compressed) return linear;
    return buildCompressedScale(axis, LABEL_WIDTH, plotWidth) ?? linear;
  }, [axis, compressed, start, end, plotWidth]);

  const ticks = useMemo(() => pointTicks(burndown.total), [burndown.total]);
  const finalRemaining = burndown.remaining[burndown.remaining.length - 1]?.remaining ?? burndown.total;
  const steps = useMemo(
    () => burndown.remaining.filter((p, i) => i > 0 && p.remaining !== burndown.remaining[i - 1].remaining).length,
    [burndown.remaining],
  );
  const ceiling = Math.max(burndown.total, ticks[ticks.length - 1] ?? 1, 1);
  const y = (value: number) => TOP_PADDING + PLOT_HEIGHT * (1 - value / ceiling);

  // A sprint with no estimates has no burndown to draw, and an empty plot with
  // an axis reads as "nothing was delivered" rather than "nothing was pointed".
  if (burndown.total <= 0) {
    return (
      <div ref={ref} className="small muted" style={{ padding: "8px 0" }}>
        No story points on the work items in this scope, so there is nothing to burn down.
        {burndown.unestimated.length > 0 && ` ${burndown.unestimated.length} unestimated.`}
      </div>
    );
  }

  const onMove = (event: React.MouseEvent<SVGSVGElement>) => {
    const box = event.currentTarget.getBoundingClientRect();
    const x = ((event.clientX - box.left) / box.width) * totalWidth;
    if (x < LABEL_WIDTH || x > LABEL_WIDTH + plotWidth) {
      setHover(null);
      return;
    }
    // Invert the piecewise scale by walking its segments, which is exact and
    // costs nothing at this size.
    let ms = start.getTime();
    for (const segment of scale.segments) {
      if (x <= segment.x + segment.width) {
        const ratio = (x - segment.x) / Math.max(1, segment.width);
        ms = segment.fromMs + ratio * (segment.toMs - segment.fromMs);
        break;
      }
      ms = segment.toMs;
    }
    setHover({
      x,
      ms,
      remaining: remainingAt(burndown.remaining, ms),
      ideal: remainingAt(burndown.ideal, ms),
    });
  };

  return (
    <div ref={ref} style={{ position: "relative" }}>
      <svg
        viewBox={`0 0 ${totalWidth} ${height}`}
        width="100%"
        height={height}
        role="img"
        aria-label={`Burndown: ${formatPoints(burndown.total)} points committed`}
        onMouseMove={onMove}
        onMouseLeave={() => setHover(null)}
      >
        {compressed &&
          scale.segments
            .filter((s) => s.kind === "OFF_HOURS")
            .map((s, i) => (
              <rect
                key={`off-${i}`}
                x={s.x}
                y={TOP_PADDING}
                width={s.width}
                height={PLOT_HEIGHT}
                fill="var(--offhours)"
              />
            ))}

        {/* The gutter exists to hold this chart's x-axis in line with the
            timeline's, so a drop in points sits directly under the sub-task
            row that caused it. Standing empty it reads as a rendering fault,
            so it carries the figure the chart is about. */}
        <text x={0} y={TOP_PADDING + 30} fontSize={30} fontWeight={600} fill="var(--text-primary)">
          {formatPoints(finalRemaining)}
        </text>
        <text x={0} y={TOP_PADDING + 52} fontSize={12} fill="var(--text-secondary)">
          {finalRemaining === 0 ? "points left — all burned down" : `points left of ${formatPoints(burndown.total)}`}
        </text>
        <text x={0} y={TOP_PADDING + 76} fontSize={12} fill="var(--text-muted)">
          {`${formatPoints(burndown.total - finalRemaining)} burned · ${steps} ${steps === 1 ? "item" : "items"} finished`}
        </text>

        {ticks.map((value) => (
          <g key={value}>
            <line
              x1={LABEL_WIDTH}
              x2={LABEL_WIDTH + plotWidth}
              y1={y(value)}
              y2={y(value)}
              stroke="var(--gridline)"
              strokeWidth={1}
            />
            <text
              x={LABEL_WIDTH - 8}
              y={y(value) + 4}
              textAnchor="end"
              fontSize={11}
              fill="var(--text-muted)"
            >
              {formatPoints(value)}
            </text>
          </g>
        ))}

        {/* The ideal is a reference, not a series: dashed and recessive, so the
            eye reads the real line first and the two never compete. */}
        <path
          d={path(burndown.ideal, scale, y)}
          fill="none"
          stroke="var(--text-muted)"
          strokeWidth={2}
          strokeDasharray="5 4"
          strokeLinecap="round"
        />
        <path
          d={path(burndown.remaining, scale, y)}
          fill="none"
          stroke="var(--state-in-progress)"
          strokeWidth={2}
          strokeLinejoin="round"
          strokeLinecap="round"
        />

        {hover && (
          <line
            x1={hover.x}
            x2={hover.x}
            y1={TOP_PADDING}
            y2={TOP_PADDING + PLOT_HEIGHT}
            stroke="var(--text-muted)"
            strokeWidth={1}
          />
        )}

        <line
          x1={LABEL_WIDTH}
          x2={LABEL_WIDTH + plotWidth}
          y1={TOP_PADDING + PLOT_HEIGHT}
          y2={TOP_PADDING + PLOT_HEIGHT}
          stroke="var(--border)"
          strokeWidth={1}
        />
      </svg>

      {hover && (
        <div
          role="tooltip"
          style={{
            position: "absolute",
            left: `${(hover.x / totalWidth) * 100}%`,
            top: TOP_PADDING,
            transform: hover.x > LABEL_WIDTH + plotWidth * 0.7 ? "translateX(-104%)" : "translateX(12px)",
            pointerEvents: "none",
            background: "var(--surface)",
            border: "1px solid var(--border)",
            borderRadius: 8,
            padding: "8px 10px",
            fontSize: 12,
            whiteSpace: "nowrap",
            boxShadow: "0 6px 20px rgba(0,0,0,0.14)",
          }}
        >
          <div className="small">
            {new Date(hover.ms).toLocaleString(undefined, {
              month: "short",
              day: "numeric",
              hour: "numeric",
            })}
          </div>
          <div className="small">
            <strong>{hover.remaining === null ? "—" : formatPoints(hover.remaining)}</strong> left
            {hover.ideal !== null && (
              <span className="muted"> · {formatPoints(hover.ideal)} ideal</span>
            )}
          </div>
        </div>
      )}
    </div>
  );
}
