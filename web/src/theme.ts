/**
 * The colour theme preference.
 *
 * Three states, not two. "System" is a real choice and the default: a reader
 * whose machine switches to dark in the evening keeps that behaviour unless
 * they say otherwise. A two-state toggle would strand them on whichever side
 * they last pressed, with no way back to following the machine.
 *
 * theme.css reads the choice from a `data-theme` attribute on the root element
 * and treats its absence as "system" — the light tokens sit on bare `:root`,
 * the dark ones under both `prefers-color-scheme` and an explicit stamp. So
 * "system" is the attribute being gone, not an attribute value.
 *
 * The stored choice is also applied by an inline script in index.html, before
 * first paint. That script and this module share THEME_STORAGE_KEY and must
 * agree on it.
 */
export const THEME_STORAGE_KEY = "delivery-analytics.theme";

export type ThemePreference = "system" | "light" | "dark";

export function readThemePreference(): ThemePreference {
  let stored: string | null = null;
  try {
    stored = localStorage.getItem(THEME_STORAGE_KEY);
  } catch {
    // Storage can be refused outright — a private window, or site data blocked.
    // Following the machine is the right answer when we cannot remember.
  }
  return stored === "light" || stored === "dark" ? stored : "system";
}

export function applyThemePreference(preference: ThemePreference): void {
  const root = document.documentElement;
  if (preference === "system") root.removeAttribute("data-theme");
  else root.setAttribute("data-theme", preference);

  try {
    if (preference === "system") localStorage.removeItem(THEME_STORAGE_KEY);
    else localStorage.setItem(THEME_STORAGE_KEY, preference);
  } catch {
    // A choice that cannot be remembered still applies for this visit.
  }
}
