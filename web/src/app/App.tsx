import { FormEvent, useCallback, useEffect, useMemo, useState } from "react";
import { Send, SlidersHorizontal } from "lucide-react";
import { ArtifactWorkspace } from "../artifacts/ArtifactWorkspace";
import { ActivityCard } from "../components/ai-elements/ActivityCard";
import { Button } from "../components/ui/button";
import { fetchArtifact, fetchArtifacts, fetchEvents, fetchTask } from "../lib/api";
import type { ArtifactInfo, TaskStatus } from "../lib/types";
import { connectTaskEvents } from "../stream/eventSource";
import { addLocalActivity, applyServerEvent, createInitialTaskState, type TaskState } from "../task-store/store";

export function App() {
  const [taskId, setTaskId] = useState("task_smoke");
  const [draftTaskId, setDraftTaskId] = useState("task_smoke");
  const [state, setState] = useState<TaskState>(() => createInitialTaskState("task_smoke"));
  const [input, setInput] = useState("");
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

  const refreshTask = useCallback(async () => {
    try {
      const [status, events, artifacts] = await Promise.all([
        fetchTask(taskId),
        fetchEvents(taskId),
        fetchArtifacts(taskId)
      ]);
      setState((current) => ({
        ...current,
        taskId,
        status,
        artifacts,
        activity: events.map((event) => applyServerEvent(createInitialTaskState(taskId), event).activity[0])
      }));
      setError("");
    } catch (err) {
      setState(createInitialTaskState(taskId));
      setError(errorMessage(err));
    }
  }, [taskId]);

  useEffect(() => {
    void refreshTask();
    const disconnect = connectTaskEvents(taskId, (event) => {
      setState((current) => applyServerEvent(current, event));
      void refreshArtifacts();
    });
    return disconnect;
  }, [refreshArtifacts, refreshTask, taskId]);

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

  const submit = (event: FormEvent) => {
    event.preventDefault();
    const value = input.trim();
    if (!value) {
      return;
    }
    if (value.startsWith("/task ")) {
      const nextTask = value.slice(6).trim();
      setTaskId(nextTask);
      setDraftTaskId(nextTask);
      setInput("");
      return;
    }
    if (value.startsWith("/artifact ")) {
      void selectArtifact(value.slice(10).trim());
      setInput("");
      return;
    }
    setState((current) => addLocalActivity(current, value));
    setInput("");
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
          <Button title="Load task" onClick={() => setTaskId(draftTaskId)}>
            <SlidersHorizontal size={16} />
            Load
          </Button>
        </header>

        <div className="task-strip">
          <label>
            <span>Task</span>
            <input value={draftTaskId} onChange={(event) => setDraftTaskId(event.target.value)} />
          </label>
          <StatusPill status={state.status} dirtyCount={dirtyCount} />
        </div>

        {error ? <div className="error-line">{error}</div> : null}

        <div className="activity-list">
          {state.activity.length === 0 ? (
            <div className="empty">No task events loaded.</div>
          ) : (
            state.activity.map((item) => <ActivityCard item={item} key={item.id} />)
          )}
        </div>

        <form className="composer" onSubmit={submit}>
          <textarea
            value={input}
            onChange={(event) => setInput(event.target.value)}
            placeholder="/task task_123, /artifact implementation.mdx, or local note"
          />
          <Button title="Send" type="submit">
            <Send size={16} />
          </Button>
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

function StatusPill({ status, dirtyCount }: { status: TaskStatus | null; dirtyCount: number }) {
  if (!status) {
    return <div className="status-pill muted">offline</div>;
  }
  return (
    <div className={dirtyCount > 0 ? "status-pill dirty" : "status-pill"}>
      {status.Task.State} · {status.NextAction} · dirty {dirtyCount}
    </div>
  );
}

function errorMessage(err: unknown): string {
  return err instanceof Error ? err.message : "unknown error";
}
