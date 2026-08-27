import type { Parent } from "../types";
import { stateColor, stateLabel } from "../types";

/**
 * The table view. It exists so the chart is never the only way to read the
 * data: three of the seven states sit below 3:1 contrast on one surface or the
 * other, and a reader who cannot separate two colours needs somewhere to go.
 */
export function TimelineTable({ parents }: { parents: Parent[] }) {
  return (
    <div className="scroll-x">
      <table className="data">
        <thead>
          <tr>
            <th scope="col">Parent</th>
            <th scope="col">Row</th>
            <th scope="col">State</th>
            <th scope="col">From</th>
            <th scope="col">To</th>
          </tr>
        </thead>
        <tbody>
          {parents.flatMap((parent) =>
            parent.rows.flatMap((row, rowIndex) =>
              row.intervals.map((interval, index) => (
                <tr key={`${parent.key}-${rowIndex}-${index}`}>
                  <td>{index === 0 ? parent.key : ""}</td>
                  <td>{index === 0 ? (row.kind === "BRANCH" ? row.label : row.key) : ""}</td>
                  <td>
                    <span className="row" style={{ gap: 6 }}>
                      <span
                        className="swatch"
                        style={{ background: stateColor(interval.state) }}
                        aria-hidden="true"
                      />
                      {stateLabel(interval.state)}
                    </span>
                  </td>
                  <td>{new Date(interval.from).toLocaleString()}</td>
                  <td>{new Date(interval.to).toLocaleString()}</td>
                </tr>
              )),
            ),
          )}
        </tbody>
      </table>
    </div>
  );
}
