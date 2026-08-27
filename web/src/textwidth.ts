/**
 * Text measurement for the SVG charts.
 *
 * The labels were previously cut to a fixed number of characters, which is a
 * guess about one particular gutter width. With the gutter adjustable, and with
 * the reader dragging it, a guess that overflows or leaves a visible gap reads
 * as a rendering fault — so the labels are measured against the width they
 * actually have.
 */

/** The family `body` sets in theme.css. The charts inherit it. */
const FAMILY = 'ui-sans-serif, system-ui, -apple-system, "Segoe UI", Roboto, sans-serif';

export function chartFont(sizePx: number, weight = 400): string {
  return `${weight} ${sizePx}px ${FAMILY}`;
}

let context: CanvasRenderingContext2D | null | undefined;

function measurer(): CanvasRenderingContext2D | null {
  if (context === undefined) {
    // A canvas the document never sees, used only for its text metrics. It can
    // be refused — a locked-down browser, a non-DOM test environment — and the
    // fallback below is an estimate rather than a failure.
    context = document.createElement("canvas").getContext("2d") ?? null;
  }
  return context;
}

/** The rendered width of `text`, in CSS pixels. */
export function measure(text: string, font: string): number {
  const ctx = measurer();
  if (!ctx) return text.length * 6.2; // A rough average for a 12px sans face.
  ctx.font = font;
  return ctx.measureText(text).width;
}

const ELLIPSIS = "…";

/**
 * `text` if it fits in `maxWidth`, otherwise the longest prefix that fits with
 * an ellipsis appended.
 *
 * The common case is one measurement: most labels fit, and the binary search
 * runs only for the ones that do not.
 */
export function fit(text: string, font: string, maxWidth: number): string {
  if (maxWidth <= 0) return "";
  if (measure(text, font) <= maxWidth) return text;

  let low = 0;
  let high = text.length;
  while (low < high) {
    const middle = Math.ceil((low + high) / 2);
    if (measure(text.slice(0, middle) + ELLIPSIS, font) <= maxWidth) low = middle;
    else high = middle - 1;
  }
  return low === 0 ? "" : text.slice(0, low) + ELLIPSIS;
}
