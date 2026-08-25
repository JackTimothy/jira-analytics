import { STATES, stateColor, stateLabel } from "../types";

/**
 * Always present: with seven states, identity must never rest on colour alone.
 * The order mirrors the progression the states describe, so the legend doubles
 * as an explanation of the workflow.
 */
export function Legend() {
  return (
    <div className="legend" aria-label="Timeline states">
      {STATES.map((state) => (
        <span className="legend-item" key={state}>
          <span className="swatch" style={{ background: stateColor(state) }} aria-hidden="true" />
          {stateLabel(state)}
        </span>
      ))}
    </div>
  );
}
