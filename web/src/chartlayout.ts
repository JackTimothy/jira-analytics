/**
 * Layout shared by the timeline and the burndown.
 *
 * The two charts stack into one reading — a drop in points sits directly under
 * the row whose merge caused it — which only works while they agree on where
 * the plot starts. So the gutter width is one value, owned above them and
 * passed to both, and these constants live here rather than being copied into
 * each chart as they used to be.
 */

export const PADDING_RIGHT = 16;

/** Below this the plot stops being a timeline and starts being a smear. */
export const MIN_PLOT_WIDTH = 520;

export const GUTTER_MIN = 200;
export const GUTTER_MAX = 560;

export const GUTTER_STORAGE_KEY = "delivery-analytics.labelWidth";

/**
 * The gutter when the reader has not chosen one: a share of the chart, so a
 * wider window means longer titles with nothing to learn and nothing to drag.
 */
export function defaultGutter(containerWidth: number): number {
  return clampGutter(Math.round(containerWidth * 0.3), containerWidth);
}

/**
 * Holds a requested gutter inside what the chart can actually give it.
 *
 * The plot floor wins over GUTTER_MAX, and GUTTER_MIN wins over the plot floor:
 * on a narrow window there is no width that satisfies both, and the honest
 * answer there is a readable gutter and a chart that scrolls, which is what it
 * did before the gutter was adjustable.
 */
export function clampGutter(requested: number, containerWidth: number): number {
  const ceiling = Math.max(GUTTER_MIN, Math.min(GUTTER_MAX, containerWidth - MIN_PLOT_WIDTH - PADDING_RIGHT));
  return Math.max(GUTTER_MIN, Math.min(ceiling, Math.round(requested)));
}

/**
 * The reader's chosen gutter, or null for "no choice made" — the same shape as
 * the theme control's System, and read and written the same guarded way.
 */
export function readStoredGutter(): number | null {
  let stored: string | null = null;
  try {
    stored = localStorage.getItem(GUTTER_STORAGE_KEY);
  } catch {
    // Storage can be refused outright. Falling back to the responsive default
    // is the right answer when we cannot remember.
  }
  const value = Number(stored);
  return stored !== null && Number.isFinite(value) && value > 0 ? value : null;
}

export function storeGutter(width: number | null): void {
  try {
    if (width === null) localStorage.removeItem(GUTTER_STORAGE_KEY);
    else localStorage.setItem(GUTTER_STORAGE_KEY, String(Math.round(width)));
  } catch {
    // A width that cannot be remembered still applies for this visit.
  }
}
