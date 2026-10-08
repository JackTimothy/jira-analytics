import type { Holiday } from "./types";

/**
 * Formats a calendar date such as "2026-11-26" — a day, not an instant.
 *
 * `new Date("2026-11-26")` reads the string as midnight UTC, which every
 * timezone west of Greenwich then shows as the 25th. Building the date from
 * its parts puts it on the same day wherever the browser is.
 */
export function formatCalendarDate(date: string, options: Intl.DateTimeFormatOptions): string {
  const [year, month, day] = date.split("-").map(Number);
  return new Date(year, month - 1, day).toLocaleDateString(undefined, options);
}

/** ISO dates sort correctly as strings. */
export function sortHolidays(holidays: Holiday[]): Holiday[] {
  return [...holidays].sort((a, b) => a.date.localeCompare(b.date));
}
