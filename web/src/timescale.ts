import type { AxisSegment } from "./types";

/**
 * A piecewise time scale: working segments share the plot width in proportion
 * to their duration, while every off-hours segment collapses to one fixed
 * narrow band regardless of length. A weeknight and a weekend look the same,
 * which is honest about the fact that neither is being shown to scale.
 */
export const OFF_HOURS_WIDTH = 14;

interface ScaleSegment {
  fromMs: number;
  toMs: number;
  x: number;
  width: number;
  kind: "WORKING" | "OFF_HOURS";
}

export interface TimeScale {
  (iso: string): number;
  segments: ScaleSegment[];
}

export function buildLinearScale(startMs: number, endMs: number, x0: number, width: number): TimeScale {
  const span = Math.max(1, endMs - startMs);
  const scale = ((iso: string) => {
    const ratio = (new Date(iso).getTime() - startMs) / span;
    return x0 + Math.max(0, Math.min(1, ratio)) * width;
  }) as TimeScale;
  scale.segments = [{ fromMs: startMs, toMs: endMs, x: x0, width, kind: "WORKING" }];
  return scale;
}

export function buildCompressedScale(axis: AxisSegment[], x0: number, width: number): TimeScale | null {
  if (axis.length === 0) return null;

  const parsed = axis.map((s) => ({
    fromMs: new Date(s.from).getTime(),
    toMs: new Date(s.to).getTime(),
    kind: s.kind,
  }));

  const offCount = parsed.filter((s) => s.kind === "OFF_HOURS").length;
  const workingTotal = parsed
    .filter((s) => s.kind === "WORKING")
    .reduce((sum, s) => sum + (s.toMs - s.fromMs), 0);
  const workingWidth = width - offCount * OFF_HOURS_WIDTH;

  // A degenerate axis — no working time at all, or bands wider than the plot —
  // cannot be compressed sensibly; the caller falls back to linear.
  if (workingTotal <= 0 || workingWidth <= 0) return null;

  const segments: ScaleSegment[] = [];
  let x = x0;
  for (const s of parsed) {
    const w =
      s.kind === "OFF_HOURS" ? OFF_HOURS_WIDTH : ((s.toMs - s.fromMs) / workingTotal) * workingWidth;
    segments.push({ ...s, x, width: w });
    x += w;
  }

  const scale = ((iso: string) => {
    const t = new Date(iso).getTime();
    if (t <= segments[0].fromMs) return segments[0].x;
    const last = segments[segments.length - 1];
    if (t >= last.toMs) return last.x + last.width;
    for (const s of segments) {
      if (t <= s.toMs) {
        const ratio = (t - s.fromMs) / Math.max(1, s.toMs - s.fromMs);
        return s.x + ratio * s.width;
      }
    }
    return last.x + last.width;
  }) as TimeScale;
  scale.segments = segments;
  return scale;
}
