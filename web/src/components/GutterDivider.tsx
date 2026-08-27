import { useRef } from "react";

import { GUTTER_MAX, GUTTER_MIN } from "../chartlayout";

const NUDGE = 16;

/**
 * The boundary between the chart's labels and its plot, draggable.
 *
 * A real control rather than a decorated div: it reports itself as a separator
 * carrying a value, so a reader who is not holding a mouse can find it and move
 * it. Double-click gives back the responsive default.
 *
 * `onChange` fires continuously while dragging and `onCommit` once the gesture
 * ends, which is what keeps the width from being written to storage sixty times
 * a second on the way past.
 */
export function GutterDivider({
  x,
  height,
  onChange,
  onCommit,
  onReset,
}: {
  x: number;
  height: number;
  onChange: (width: number) => void;
  onCommit: () => void;
  onReset: () => void;
}) {
  const frame = useRef(0);

  // Pointer moves arrive faster than the chart can be laid out, so only the
  // last one in a frame is acted on.
  function schedule(clientX: number, scroller: HTMLElement | null) {
    if (!scroller) return;
    cancelAnimationFrame(frame.current);
    frame.current = requestAnimationFrame(() => {
      const box = scroller.getBoundingClientRect();
      onChange(clientX - box.left + scroller.scrollLeft);
    });
  }

  return (
    <div
      className="gutter-divider"
      style={{ left: x, height }}
      role="separator"
      aria-orientation="vertical"
      aria-label="Width of the chart's label column"
      aria-valuenow={Math.round(x)}
      aria-valuemin={GUTTER_MIN}
      aria-valuemax={GUTTER_MAX}
      tabIndex={0}
      onPointerDown={(event) => {
        // Stops the drag from selecting the labels it passes over; focus is
        // then taken deliberately, since preventDefault would have skipped it.
        event.preventDefault();
        event.currentTarget.focus();
        event.currentTarget.setPointerCapture(event.pointerId);
      }}
      onPointerMove={(event) => {
        if (!event.currentTarget.hasPointerCapture(event.pointerId)) return;
        schedule(event.clientX, event.currentTarget.parentElement);
      }}
      onPointerUp={(event) => {
        event.currentTarget.releasePointerCapture(event.pointerId);
        onCommit();
      }}
      onDoubleClick={onReset}
      onKeyDown={(event) => {
        if (event.key === "ArrowLeft") onChange(x - NUDGE);
        else if (event.key === "ArrowRight") onChange(x + NUDGE);
        else if (event.key === "Home") onChange(GUTTER_MIN);
        else if (event.key === "End") onChange(GUTTER_MAX);
        else return;
        event.preventDefault();
      }}
      onKeyUp={onCommit}
    />
  );
}
