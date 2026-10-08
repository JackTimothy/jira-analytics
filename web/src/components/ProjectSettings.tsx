import { useState } from "react";

import { api } from "../api";
import { formatCalendarDate, sortHolidays } from "../calendar";
import type { Holiday, Project, WorkingHours } from "../types";

const ALL_DAYS = [
  "monday",
  "tuesday",
  "wednesday",
  "thursday",
  "friday",
  "saturday",
  "sunday",
] as const;

/**
 * The settings that shape every retrospective: the timezone decides which
 * calendar day a sprint's end falls on (and so which work counts as
 * committed), and the working hours and holidays decide which parts of the
 * axis are shown to scale and what counts toward cycle time. The server
 * rejects invalid values rather than defaulting, so errors are surfaced here
 * rather than swallowed.
 */
export function ProjectSettings({
  project,
  onChange,
}: {
  project: Project;
  onChange: (project: Project) => void;
}) {
  const [timezone, setTimezone] = useState(project.settings.timezone);
  const [hours, setHours] = useState<WorkingHours>(project.settings.workingHours);
  const [holidays, setHolidays] = useState<Holiday[]>(sortHolidays(project.settings.holidays));
  const [draft, setDraft] = useState<Holiday>({ date: "", name: "" });
  const [status, setStatus] = useState<"idle" | "saving" | "saved">("idle");
  const [error, setError] = useState<string | null>(null);

  const zones = supportedTimezones(project.settings.timezone);

  async function save(patch: {
    timezone?: string;
    workingHours?: WorkingHours;
    holidays?: Holiday[];
  }): Promise<boolean> {
    setStatus("saving");
    setError(null);
    try {
      const updated = await api.updateSettings(project.id, patch);
      setTimezone(updated.settings.timezone);
      setHours(updated.settings.workingHours);
      setHolidays(sortHolidays(updated.settings.holidays));
      onChange(updated);
      setStatus("saved");
      return true;
    } catch (caught) {
      setStatus("idle");
      setTimezone(project.settings.timezone);
      setHours(project.settings.workingHours);
      setHolidays(sortHolidays(project.settings.holidays));
      setError(caught instanceof Error ? caught.message : String(caught));
      return false;
    }
  }

  function toggleDay(day: string) {
    const next = hours.days.includes(day)
      ? hours.days.filter((d) => d !== day)
      : [...hours.days, day];
    void save({ workingHours: { ...hours, days: next } });
  }

  const draftIsListed = holidays.some((holiday) => holiday.date === draft.date);
  const canAdd = draft.date !== "" && !draftIsListed && status !== "saving";

  async function addHoliday(event: React.FormEvent) {
    event.preventDefault();
    if (!canAdd) return;
    const holiday: Holiday = { date: draft.date, name: draft.name?.trim() || undefined };
    if (await save({ holidays: sortHolidays([...holidays, holiday]) })) {
      setDraft({ date: "", name: "" });
    }
  }

  function removeHoliday(date: string) {
    void save({ holidays: holidays.filter((holiday) => holiday.date !== date) });
  }

  return (
    <div className="stack">
      {/* Each field is label, control, then a caption. The captions used to sit
          inline with their labels, where they competed with the control for the
          same width and the longer of the two wrapped. Below the control they
          can say what they need to at any width. */}
      <div className="row" style={{ alignItems: "flex-start", gap: 32 }}>
        <div className="field">
          <label htmlFor="timezone">Project timezone</label>
          <select
            id="timezone"
            value={timezone}
            disabled={status === "saving"}
            onChange={(event) => void save({ timezone: event.target.value })}
          >
            {zones.map((zone) => (
              <option key={zone} value={zone}>
                {zone}
              </option>
            ))}
          </select>
          <span className="muted small">Decides which day a sprint ends on</span>
        </div>

        <div className="field" style={{ maxWidth: "none" }}>
          <span id="working-hours-label">Working hours</span>
          <div className="row" style={{ gap: 8 }} role="group" aria-labelledby="working-hours-label">
            <input
              type="time"
              aria-label="Working hours start"
              value={hours.start}
              disabled={status === "saving"}
              onChange={(event) => void save({ workingHours: { ...hours, start: event.target.value } })}
            />
            <span className="muted">to</span>
            <input
              type="time"
              aria-label="Working hours end"
              value={hours.end}
              disabled={status === "saving"}
              onChange={(event) => void save({ workingHours: { ...hours, end: event.target.value } })}
            />
            <span className="row" style={{ gap: 4 }} role="group" aria-label="Working days">
              {ALL_DAYS.map((day) => (
                <button
                  key={day}
                  className="day-chip"
                  aria-pressed={hours.days.includes(day)}
                  disabled={status === "saving"}
                  onClick={() => toggleDay(day)}
                  title={day}
                >
                  {day.slice(0, 1).toUpperCase()}
                </button>
              ))}
            </span>
          </div>
          <span className="muted small">The part of the timeline shown to scale</span>
        </div>
      </div>

      <div className="field" style={{ maxWidth: "none" }}>
        <span id="holidays-label">Holidays</span>
        <form className="row" style={{ gap: 8 }} aria-labelledby="holidays-label" onSubmit={addHoliday}>
          <input
            type="date"
            aria-label="Holiday date"
            value={draft.date}
            disabled={status === "saving"}
            onChange={(event) => setDraft({ ...draft, date: event.target.value })}
          />
          <input
            type="text"
            aria-label="Holiday name"
            placeholder="Name (optional)"
            value={draft.name}
            disabled={status === "saving"}
            onChange={(event) => setDraft({ ...draft, name: event.target.value })}
          />
          <button type="submit" className="button" disabled={!canAdd}>
            Add
          </button>
          {draftIsListed && <span className="muted small">Already a holiday</span>}
        </form>
        {holidays.length > 0 && (
          <ul className="list stack" style={{ gap: 4 }}>
            {holidays.map((holiday) => {
              const date = formatCalendarDate(holiday.date, {
                weekday: "short",
                year: "numeric",
                month: "short",
                day: "numeric",
              });
              return (
                <li key={holiday.date} className="row small">
                  <span>{date}</span>
                  {holiday.name && <span className="muted">{holiday.name}</span>}
                  <button
                    className="link-button"
                    aria-label={`Remove ${holiday.name || "the holiday"} on ${date}`}
                    disabled={status === "saving"}
                    onClick={() => removeHoliday(holiday.date)}
                  >
                    Remove
                  </button>
                </li>
              );
            })}
          </ul>
        )}
        <span className="muted small">Whole days off in the project timezone, counted as no working hours</span>
      </div>

      {status === "saved" && <p className="small muted">Saved.</p>}
      {error && (
        <p className="notice error small" role="alert">
          {error}
        </p>
      )}
    </div>
  );
}

/**
 * The browser knows the full tz database in modern engines; where it does not,
 * fall back to a short list that still includes whatever the project is already
 * set to, so the current value is never silently unselectable.
 */
function supportedTimezones(current: string): string[] {
  const withCurrent = (list: string[]) =>
    list.includes(current) ? list : [current, ...list].sort();

  const supported = (Intl as { supportedValuesOf?: (key: string) => string[] }).supportedValuesOf;
  if (typeof supported === "function") {
    try {
      return withCurrent(supported("timeZone"));
    } catch {
      // Fall through to the short list.
    }
  }
  return withCurrent([
    "America/New_York",
    "America/Chicago",
    "America/Denver",
    "America/Los_Angeles",
    "Europe/London",
    "Europe/Berlin",
    "Asia/Kolkata",
    "Asia/Tokyo",
    "Australia/Sydney",
    "UTC",
  ]);
}
