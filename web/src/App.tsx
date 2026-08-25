import { useCallback, useEffect, useState } from "react";

import { api } from "./api";
import { Legend } from "./components/Legend";
import { ProjectSettings } from "./components/ProjectSettings";
import { Timeline } from "./components/Timeline";
import { TimelineTable } from "./components/TimelineTable";
import type { Project, Retrospective, Scope, Sprint } from "./types";

type View =
  | { name: "projects" }
  | { name: "sprints"; project: Project }
  | { name: "retrospective"; project: Project; sprint: Sprint };

export function App() {
  const [view, setView] = useState<View>({ name: "projects" });

  return (
    <main className="app">
      <header className="stack" style={{ marginBottom: 20 }}>
        <h1 style={{ fontSize: 22 }}>Delivery Analytics</h1>
        <p className="muted small" style={{ margin: 0 }}>
          Sprint retrospectives assembled from your issue tracker and code host.
        </p>
      </header>

      {view.name === "projects" && (
        <ProjectsView onOpen={(project) => setView({ name: "sprints", project })} />
      )}

      {view.name === "sprints" && (
        <SprintsView
          project={view.project}
          onBack={() => setView({ name: "projects" })}
          onProjectChange={(project) => setView({ name: "sprints", project })}
          onOpen={(sprint) => setView({ name: "retrospective", project: view.project, sprint })}
        />
      )}

      {view.name === "retrospective" && (
        <RetrospectiveView
          project={view.project}
          sprint={view.sprint}
          onBack={() => setView({ name: "sprints", project: view.project })}
        />
      )}
    </main>
  );
}

/** Shared loading/error shell so every view reports failure the same way. */
function useAsync<T>(load: () => Promise<T>, deps: unknown[]) {
  const [data, setData] = useState<T | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);

  const run = useCallback(() => {
    let cancelled = false;
    setLoading(true);
    setError(null);
    load()
      .then((result) => {
        if (!cancelled) setData(result);
      })
      .catch((caught: unknown) => {
        if (!cancelled) setError(caught instanceof Error ? caught.message : String(caught));
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, deps);

  useEffect(run, [run]);
  return { data, error, loading };
}

function Status({ loading, error }: { loading: boolean; error: string | null }) {
  if (loading) return <p className="muted">Loading…</p>;
  if (error)
    return (
      <p className="notice error" role="alert">
        {error}
      </p>
    );
  return null;
}

function ProjectsView({ onOpen }: { onOpen: (project: Project) => void }) {
  const { data, error, loading } = useAsync(() => api.listProjects(), []);

  if (loading || error) return <Status loading={loading} error={error} />;

  return (
    <section className="card">
      <ul className="list">
        {(data ?? []).map((project) => (
          <li key={project.id}>
            <button className="list-item" onClick={() => onOpen(project)}>
              <span>
                <strong>{project.name}</strong>
                <br />
                <span className="muted small">
                  {project.tracker.projectKey} · {project.repos.join(", ")}
                </span>
              </span>
              <span aria-hidden="true" className="muted">
                →
              </span>
            </button>
          </li>
        ))}
      </ul>
    </section>
  );
}

function SprintsView({
  project,
  onBack,
  onOpen,
  onProjectChange,
}: {
  project: Project;
  onBack: () => void;
  onOpen: (sprint: Sprint) => void;
  onProjectChange: (project: Project) => void;
}) {
  const { data, error, loading } = useAsync(() => api.listSprints(project.id), [project.id]);

  return (
    <>
      <nav className="crumbs">
        <button className="link-button" onClick={onBack}>
          Projects
        </button>
        <span aria-hidden="true">/</span>
        <span>{project.name}</span>
      </nav>

      <section className="card" style={{ padding: 16, marginBottom: 20 }}>
        <ProjectSettings project={project} onChange={onProjectChange} />
      </section>

      <h2 style={{ fontSize: 16, marginBottom: 10 }}>Sprints</h2>
      <Status loading={loading} error={error} />

      {data && (
        <section className="card">
          <ul className="list">
            {data.map((sprint) => (
              <li key={sprint.id}>
                <button className="list-item" onClick={() => onOpen(sprint)}>
                  <span>
                    <strong>{sprint.name}</strong>
                    <br />
                    <span className="muted small">
                      {new Date(sprint.start).toLocaleDateString()} –{" "}
                      {new Date(sprint.end).toLocaleDateString()}
                    </span>
                  </span>
                  <span aria-hidden="true" className="muted">
                    →
                  </span>
                </button>
              </li>
            ))}
          </ul>
        </section>
      )}
    </>
  );
}

function RetrospectiveView({
  project,
  sprint,
  onBack,
}: {
  project: Project;
  sprint: Sprint;
  onBack: () => void;
}) {
  const [scope, setScope] = useState<Scope>("all");
  const [asTable, setAsTable] = useState(false);

  const { data, error, loading } = useAsync<Retrospective>(
    () => api.retrospective(project.id, sprint.id, scope),
    [project.id, sprint.id, scope],
  );

  return (
    <>
      <nav className="crumbs">
        <button className="link-button" onClick={onBack}>
          {project.name}
        </button>
        <span aria-hidden="true">/</span>
        <span>{sprint.name}</span>
      </nav>

      <div className="toolbar">
        <div className="row">
          <div className="segmented" role="group" aria-label="Sprint scope">
            <button aria-pressed={scope === "all"} onClick={() => setScope("all")}>
              All sub-tasks
            </button>
            <button aria-pressed={scope === "committed"} onClick={() => setScope("committed")}>
              Committed scope
            </button>
          </div>
          <span className="muted small">
            {scope === "committed"
              ? "Parents due on or before the sprint end, including carryover."
              : "Every sub-task whose parent was in the sprint."}
          </span>
        </div>

        <div className="segmented" role="group" aria-label="View">
          <button aria-pressed={!asTable} onClick={() => setAsTable(false)}>
            Chart
          </button>
          <button aria-pressed={asTable} onClick={() => setAsTable(true)}>
            Table
          </button>
        </div>
      </div>

      <Status loading={loading} error={error} />

      {data && (
        <div className="stack">
          <Legend />
          <section className="card" style={{ padding: 16 }}>
            {asTable ? (
              <TimelineTable parents={data.parents} />
            ) : (
              <Timeline parents={data.parents} sprint={data.sprint} />
            )}
          </section>

          {data.warnings.length > 0 && (
            <section className="notice">
              <strong className="small">Gaps in the data</strong>
              <ul className="small muted" style={{ margin: "6px 0 0", paddingLeft: 18 }}>
                {data.warnings.map((warning) => (
                  <li key={warning}>{warning}</li>
                ))}
              </ul>
            </section>
          )}
        </div>
      )}
    </>
  );
}
