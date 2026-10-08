import { formatFinished, formatHours } from "../cycletime";
import type { SprintCycleTime } from "../types";

function formatPoints(value: number): string {
  return Number.isInteger(value) ? String(value) : value.toFixed(1);
}

/**
 * The sprint's cycle time: on average, how many working hours each point of
 * finished work took, with the items behind the figure listed beneath it.
 *
 * The figure arrives computed, as the burndown's lines do, so the arithmetic
 * stays in the tested domain. The table is here so the reader can check it —
 * the figure is the mean of its last-but-one column, and saying so is cheaper
 * than asking anyone to take it on trust.
 */
export function CycleTimeCard({
  cycleTime,
  filtering,
}: {
  cycleTime: SprintCycleTime;
  filtering: boolean;
}) {
  const { hoursPerPoint, items, unestimated, unstarted } = cycleTime;

  return (
    <section className="card" style={{ padding: 16 }}>
      <strong className="small">Cycle time</strong>

      {hoursPerPoint === null ? (
        <p className="muted" style={{ margin: "10px 0 0" }}>
          No Stories, Tasks or Bugs reached Done during this sprint.
        </p>
      ) : (
        <>
          <div style={{ margin: "10px 0 14px" }}>
            <div style={{ fontSize: 30, fontWeight: 600, lineHeight: 1.2 }}>
              {formatHours(hoursPerPoint)}
            </div>
            <div className="small muted">per story point</div>
            <div className="small" style={{ color: "var(--text-muted)" }}>
              mean of {items.length}{" "}
              {items.length === 1 ? "Story, Task or Bug" : "Stories, Tasks and Bugs"} finished this
              sprint · working hours only, first In Progress to Done
            </div>
          </div>

          <div className="scroll-x">
            <table className="data">
              <thead>
                <tr>
                  <th scope="col">Work item</th>
                  <th scope="col">Type</th>
                  <th scope="col" style={{ textAlign: "right" }}>
                    Points
                  </th>
                  <th scope="col" style={{ textAlign: "right" }}>
                    Cycle time
                  </th>
                  <th scope="col" style={{ textAlign: "right" }}>
                    Per point
                  </th>
                  <th scope="col">Finished</th>
                </tr>
              </thead>
              <tbody>
                {items.map((item) => (
                  <tr key={item.key}>
                    <td>
                      {item.key} <span className="muted">{item.summary}</span>
                    </td>
                    <td>{item.type}</td>
                    <td style={{ textAlign: "right" }}>{formatPoints(item.points)}</td>
                    <td style={{ textAlign: "right" }}>{formatHours(item.hours)}</td>
                    <td style={{ textAlign: "right" }}>{formatHours(item.hoursPerPoint)}</td>
                    <td>{formatFinished(item.finished)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </>
      )}

      {filtering && (
        <p className="small muted" style={{ margin: "10px 0 0" }}>
          The filter narrows the timeline only — cycle time covers the whole scope.
        </p>
      )}
      {unestimated.length > 0 && (
        <p className="small muted" style={{ margin: "10px 0 0" }}>
          Not counted, no estimate: {unestimated.join(", ")}
        </p>
      )}
      {unstarted.length > 0 && (
        <p className="small muted" style={{ margin: "10px 0 0" }}>
          Not counted, never marked In Progress: {unstarted.join(", ")}
        </p>
      )}
    </section>
  );
}
