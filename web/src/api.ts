import type { Project, Retrospective, Scope, Sprint } from "./types";

/**
 * The API client. Errors carry the server's message where there is one, so a
 * rejected timezone or an unknown sprint reaches the user as an explanation
 * rather than "request failed".
 */
async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await fetch(path, {
    ...init,
    headers: { "Content-Type": "application/json", ...(init?.headers ?? {}) },
  });

  if (!response.ok) {
    let message = `Request failed (${response.status})`;
    try {
      const body = await response.json();
      if (body && typeof body.error === "string") message = body.error;
    } catch {
      // The body was not JSON; the status-based message stands.
    }
    throw new Error(message);
  }
  return (await response.json()) as T;
}

export const api = {
  listProjects: () => request<Project[]>("/api/v1/projects"),

  listSprints: (projectId: string) =>
    request<Sprint[]>(`/api/v1/projects/${encodeURIComponent(projectId)}/sprints`),

  retrospective: (projectId: string, sprintId: string, scope: Scope) =>
    request<Retrospective>(
      `/api/v1/projects/${encodeURIComponent(projectId)}/sprints/${encodeURIComponent(sprintId)}` +
        `/retrospective?scope=${scope}`,
    ),

  updateTimezone: (projectId: string, timezone: string) =>
    request<Project>(`/api/v1/projects/${encodeURIComponent(projectId)}/settings`, {
      method: "PATCH",
      body: JSON.stringify({ timezone }),
    }),
};
