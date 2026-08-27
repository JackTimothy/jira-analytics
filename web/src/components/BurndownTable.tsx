import type { Burndown } from "../types";

/**
 * The table view of the burndown: one row per step, which is one row per work
 * item finished. It carries what the chart cannot — the exact instant and the
 * exact drop — and it is how the numbers are read without colour or geometry.
 */
export function BurndownTable({ burndown }: { burndown: Burndown }) {
  // Only the vertices where the line actually moves are worth a row; the
  // duplicated vertex that draws each step is geometry, not data.
  const steps = burndown.remaining
    .map((point, i) => ({ point, previous: burndown.remaining[i - 1] }))
    .filter(({ point, previous }) => previous && point.remaining !== previous.remaining);

  return (
    <div className="scroll-x">
      <table className="data">
        <caption className="small muted" style={{ captionSide: "top", textAlign: "left", paddingBottom: 8 }}>
          {burndown.total} points committed · {steps.length} work {steps.length === 1 ? "item" : "items"} finished
        </caption>
        <thead>
          <tr>
            <th>When</th>
            <th style={{ textAlign: "right" }}>Burned</th>
            <th style={{ textAlign: "right" }}>Remaining</th>
          </tr>
        </thead>
        <tbody>
          {steps.length === 0 && (
            <tr>
              <td colSpan={3} className="muted">
                Nothing was finished during this sprint.
              </td>
            </tr>
          )}
          {steps.map(({ point, previous }) => (
            <tr key={`${point.at}-${point.remaining}`}>
              <td>{new Date(point.at).toLocaleString(undefined, { month: "short", day: "numeric", hour: "numeric", minute: "2-digit" })}</td>
              <td style={{ textAlign: "right" }}>
                {previous!.remaining > point.remaining ? "−" : "+"}
                {Math.abs(previous!.remaining - point.remaining)}
              </td>
              <td style={{ textAlign: "right" }}>{point.remaining}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
