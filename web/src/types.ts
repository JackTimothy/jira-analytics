/** Mirrors the API's wire contract. */

export const STATES = [
  "TO_DO",
  "IN_PROGRESS",
  "REVIEW_REQUESTED",
  "FEEDBACK_GIVEN",
  "APPROVED",
  "BLOCKED",
  "DONE",
] as const;

export type State = (typeof STATES)[number];

export interface WorkingHours {
  days: string[];
  start: string; // "08:00"
  end: string; // "18:00"
}

export interface Project {
  id: string;
  name: string;
  settings: { timezone: string; workingHours: WorkingHours };
  tracker: { projectKey: string; boardId: string };
  repos: string[];
}

export interface Sprint {
  id: string;
  name: string;
  start: string;
  end: string;
}

export interface Interval {
  state: State;
  from: string;
  to: string;
}

export interface SubTask {
  key: string;
  summary: string;
  intervals: Interval[];
}

export interface Parent {
  key: string;
  summary: string;
  dueDate: string | null;
  inScope: boolean;
  subtasks: SubTask[];
}

export type AxisSegmentKind = "WORKING" | "OFF_HOURS";

export interface AxisSegment {
  from: string;
  to: string;
  kind: AxisSegmentKind;
}

export interface Retrospective {
  sprint: Sprint;
  parents: Parent[];
  warnings: string[];
  axis: AxisSegment[];
}

export type Scope = "all" | "committed";

/**
 * Display labels and the CSS variable each state paints with. Kept beside the
 * state list so a new state cannot be added without giving it both.
 */
export const STATE_META: Record<State, { label: string; cssVar: string }> = {
  TO_DO: { label: "To Do", cssVar: "--state-to-do" },
  IN_PROGRESS: { label: "In Progress", cssVar: "--state-in-progress" },
  REVIEW_REQUESTED: { label: "Review Requested", cssVar: "--state-review-requested" },
  FEEDBACK_GIVEN: { label: "Feedback Given", cssVar: "--state-feedback-given" },
  APPROVED: { label: "Approved", cssVar: "--state-approved" },
  BLOCKED: { label: "Blocked", cssVar: "--state-blocked" },
  DONE: { label: "Done", cssVar: "--state-done" },
};

export function stateColor(state: State): string {
  return `var(${STATE_META[state].cssVar})`;
}

export function stateLabel(state: State): string {
  return STATE_META[state].label;
}
