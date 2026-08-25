import { useState } from "react";

import { api } from "../api";
import type { Project } from "../types";

/**
 * The timezone decides which calendar day a sprint's end falls on, and so which
 * work counts as committed. It is the one setting a user can change, and the
 * server rejects an unrecognised zone rather than defaulting, so the error is
 * surfaced here rather than swallowed.
 */
export function ProjectSettings({
  project,
  onChange,
}: {
  project: Project;
  onChange: (project: Project) => void;
}) {
  const [value, setValue] = useState(project.settings.timezone);
  const [status, setStatus] = useState<"idle" | "saving" | "saved">("idle");
  const [error, setError] = useState<string | null>(null);

  const zones = supportedTimezones(project.settings.timezone);

  async function save(next: string) {
    setValue(next);
    setStatus("saving");
    setError(null);
    try {
      const updated = await api.updateTimezone(project.id, next);
      onChange(updated);
      setStatus("saved");
    } catch (caught) {
      setStatus("idle");
      setValue(project.settings.timezone);
      setError(caught instanceof Error ? caught.message : String(caught));
    }
  }

  return (
    <div className="stack">
      <div className="field">
        <label htmlFor="timezone">
          Project timezone
          <span className="muted small">
            {" "}
            — decides which day a sprint ends on, and so which work counts as committed
          </span>
        </label>
        <select
          id="timezone"
          value={value}
          disabled={status === "saving"}
          onChange={(event) => void save(event.target.value)}
        >
          {zones.map((zone) => (
            <option key={zone} value={zone}>
              {zone}
            </option>
          ))}
        </select>
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
