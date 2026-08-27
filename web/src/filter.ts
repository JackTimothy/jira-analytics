import type { Parent } from "./types";

/**
 * The row filter.
 *
 * Kept apart from the components on purpose. Timeline and TimelineTable draw
 * whatever list of parents they are handed and know nothing about searching, so
 * the two views cannot drift apart in what they consider a match.
 */

/** Everything about a work item that is worth matching against. */
function parentText(parent: Parent): string {
  return `${parent.key} ${parent.summary} ${parent.type}`.toLowerCase();
}

function matchesAll(haystack: string, terms: string[]): boolean {
  return terms.every((term) => haystack.includes(term));
}

/**
 * Hides rows that do not match the query, keeping a matched work item whole.
 *
 * Every term must match, so "picker api" narrows rather than widens. A work
 * item that matches on its own key, summary or issue type keeps all of its
 * rows: typing an issue key should show that item, not a heading over nothing.
 * Otherwise each row is tested against its own text together with its parent's,
 * so "PROJ-1 api" finds one row inside one work item.
 *
 * An empty query returns the very same array rather than a copy, so the
 * ordinary unfiltered case costs nothing and cannot trigger a re-layout.
 */
export function filterParents(parents: Parent[], query: string): Parent[] {
  const terms = query.toLowerCase().split(/\s+/).filter(Boolean);
  if (terms.length === 0) return parents;

  const kept: Parent[] = [];
  for (const parent of parents) {
    const parentHaystack = parentText(parent);
    if (matchesAll(parentHaystack, terms)) {
      kept.push(parent);
      continue;
    }

    const rows = parent.rows.filter((row) =>
      matchesAll(`${parentHaystack} ${row.key} ${row.label}`.toLowerCase(), terms),
    );
    if (rows.length > 0) kept.push({ ...parent, rows });
  }
  return kept;
}
