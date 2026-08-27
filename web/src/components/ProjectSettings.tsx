import { useState } from "react";

import { api } from "../api";
import type { Project, WorkingHours } from "../types";

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
 * The two settings that shape every retrospective: the timezone decides which
 * calendar day a sprint's end falls on (and so which work counts as
 * committed), and the working hours decide which parts of the axis are shown
 * to scale. The server rejects invalid values rather than defaulting, so
 * errors are surfaced here rather than swallowed.
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
  const [status, setStatus] = useState<"idle" | "saving" | "saved">("idle");
  const [error, setError] = useState<string | null>(null);

  const zones = supportedTimezones(project.settings.timezone);

  async function save(patch: { timezone?: string; workingHours?: WorkingHours }) {
    setStatus("saving");
    setError(null);
    try {
      const updated = await api.updateSettings(project.id, patch);
      setTimezone(updated.settings.timezone);
      setHours(updated.settings.workingHours);
      onChange(updated);
      setStatus("saved");
    } catch (caught) {
      setStatus("idle");
      setTimezone(project.settings.timezone);
      setHours(project.settings.workingHours);
      setError(caught instanceof Error ? caught.message : String(caught));
    }
  }

  function toggleDay(day: string) {
    const next = hours.days.includes(day)
      ? hours.days.filter((d) => d !== day)
      : [...hours.days, day];
    void save({ workingHours: { ...hours, days: next } });
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
