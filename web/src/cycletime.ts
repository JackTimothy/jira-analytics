/**
 * How cycle time is written wherever it appears.
 *
 * One place, so the timeline heading, the table and the cycle time card cannot
 * disagree about rounding: a figure shown as 18.5 in one and 18 in another
 * reads as two different measurements.
 */

/** Working hours to one decimal place: "18.5 h". */
export function formatHours(hours: number): string {
  return `${hours.toFixed(1)} h`;
}

/** The instant an item finished, short enough for a table cell. */
export function formatFinished(iso: string): string {
  return new Date(iso).toLocaleString(undefined, {
    month: "short",
    day: "numeric",
    hour: "numeric",
    minute: "2-digit",
  });
}
