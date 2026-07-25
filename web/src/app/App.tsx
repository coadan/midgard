import { FormEvent, useCallback, useEffect, useMemo, useState } from "react";
import { Play, Plus, SlidersHorizontal } from "lucide-react";
import { ArtifactWorkspace } from "../artifacts/ArtifactWorkspace";
import { ActivityCard } from "../components/ai-elements/ActivityCard";
import { Button } from "../components/ui/button";
import { createTask, fetchArtifact, fetchArtifacts, fetchEvents, fetchTask, runTask } from "../lib/api";
import type { ArtifactInfo, TaskStatus } from "../lib/types";
import { applyServerEvent, createInitialTaskState, type TaskState } from "../task-store/store";

export function App() {
  const initialTaskId = useMemo(() => newTaskId(), []);
  const [taskId, setTaskId] = useState(initialTaskId);
  const [draftTaskId, setDraftTaskId] = useState(initialTaskId);
  const [state, setState] = useState<TaskState>(() => createInitialTaskState(initialTaskId));
  const [objective, setObjective] = useState("");
  const [running, setRunning] = useState(false);
  const [artifactLoading, setArtifactLoading] = useState(false);
  const [error, setError] = useState("");

  const refreshArtifacts = useCallback(async () => {
    try {
      const artifacts = await fetchArtifacts(taskId);
      setState((current) => ({ ...current, artifacts }));
    } catch (err) {
      setError(errorMessage(err));
    }
  }, [taskId]);

  const loadTask = useCallback(async (id: string) => {
    try {
      const [status, events, artifacts] = await Promise.all([
        fetchTask(id),
        fetchEvents(id),
        fetchArtifacts(id)
      ]);
      setState((current) => ({
        ...current,
        taskId: id,
        status,
        artifacts,
        activity: events.map((event) => applyServerEvent(createInitialTaskState(id), event).activity[0])
      }));
      setError("");
    } catch (err) {
      setState(createInitialTaskState(id));
      setError(errorMessage(err));
    }
  }, []);

  const refreshTask = useCallback(() => loadTask(taskId), [loadTask, taskId]);

  useEffect(() => {
    if (!running) {
      return;
    }
    const timer = window.setInterval(() => {
      void refreshTask();
    }, 1500);
    return () => window.clearInterval(timer);
  }, [refreshTask, running]);

  const selectArtifact = useCallback(
    async (path: string) => {
      setArtifactLoading(true);
      setState((current) => ({ ...current, selectedArtifact: path }));
      try {
        const text = await fetchArtifact(taskId, path);
        setState((current) => ({ ...current, artifactText: text }));
      } catch (err) {
        setError(errorMessage(err));
      } finally {
        setArtifactLoading(false);
      }
    },
    [taskId]
  );

  const createAndRun = async (event: FormEvent) => {
    event.preventDefault();
    const value = objective.trim();
    const id = draftTaskId.trim() || newTaskId();
    if (!value || running) {
      return;
    }
    setRunning(true);
    setError("");
    try {
      await createTask(id, value);
      setTaskId(id);
      setDraftTaskId(id);
      setState(createInitialTaskState(id));
      await runTask(id);
      await loadTask(id);
      setObjective("");
    } catch (err) {
      await loadTask(id);
      setError(errorMessage(err));
    } finally {
      setRunning(false);
    }
  };

  const dirtyCount = useMemo(() => state.status?.Worktrees?.filter((worktree) => worktree.Dirty).length ?? 0, [state.status]);

  return (
    <main className="app-shell">
      <section className="task-rail" aria-label="Task activity">
        <header className="topbar">
          <div>
            <p className="eyebrow">Midgard</p>
            <h1>Task Harness</h1>
          </div>
          <Button
            title="Load task"
            onClick={() => {
              setTaskId(draftTaskId);
              void loadTask(draftTaskId);
            }}
          >
            <SlidersHorizontal size={16} />
            Load
          </Button>
        </header>

        <div className="task-strip">
          <label>
            <span>Task</span>
            <input value={draftTaskId} onChange={(event) => setDraftTaskId(event.target.value)} />
          </label>
          <StatusPill status={state.status} dirtyCount={dirtyCount} running={running} />
        </div>

        {error ? <div className="error-line">{error}</div> : null}

        <div className="activity-list">
          {state.activity.length === 0 ? (
            <div className="empty">No task events loaded.</div>
          ) : (
            state.activity.map((item) => <ActivityCard item={item} key={item.id} />)
          )}
        </div>

        <form className="composer" onSubmit={createAndRun}>
          <textarea
            value={objective}
            onChange={(event) => setObjective(event.target.value)}
            placeholder="Describe a coding task. Midgard will create an isolated worktree and run one Codex agent."
            aria-label="Task objective"
          />
          <div className="composer-actions">
            <Button title="Create and run task" type="submit" disabled={running || !objective.trim()}>
              <Plus size={16} />
              {running ? "Working…" : "Create & run"}
            </Button>
            <Button
              title="Run loaded task"
              type="button"
              variant="ghost"
              disabled={running || !state.status}
              onClick={async () => {
                setRunning(true);
                setError("");
                try {
                  await runTask(taskId);
                  await loadTask(taskId);
                } catch (err) {
                  await loadTask(taskId);
                  setError(errorMessage(err));
                } finally {
                  setRunning(false);
                }
              }}
            >
              <Play size={16} />
              Run loaded
            </Button>
          </div>
        </form>
      </section>

      <ArtifactWorkspace
        artifacts={state.artifacts as ArtifactInfo[]}
        selected={state.selectedArtifact}
        content={state.artifactText}
        loading={artifactLoading}
        onSelect={selectArtifact}
        onRefresh={refreshArtifacts}
      />
    </main>
  );
}

function StatusPill({
  status,
  dirtyCount,
  running
}: {
  status: TaskStatus | null;
  dirtyCount: number;
  running: boolean;
}) {
  if (running) {
    return <div className="status-pill">running · Codex</div>;
  }
  if (!status) {
    return <div className="status-pill muted">offline</div>;
  }
  return (
    <div className={dirtyCount > 0 ? "status-pill dirty" : "status-pill"}>
      {status.Task.State} · {status.NextAction} · dirty {dirtyCount}
    </div>
  );
}

function newTaskId(): string {
  const stamp = new Date().toISOString().replace(/\D/g, "").slice(0, 14);
  return `task_${stamp}`;
}

function errorMessage(err: unknown): string {
  return err instanceof Error ? err.message : "unknown error";
}
