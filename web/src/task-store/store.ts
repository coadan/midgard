import type { ArtifactInfo, ServerEvent, TaskStatus } from "../lib/types";

export type ActivityItem = {
  id: string;
  kind: "event" | "command" | "check" | "local";
  title: string;
  detail: string;
};

export type TaskState = {
  taskId: string;
  status: TaskStatus | null;
  artifacts: ArtifactInfo[];
  selectedArtifact: string;
  artifactText: string;
  activity: ActivityItem[];
};

export function createInitialTaskState(taskId: string): TaskState {
  return {
    taskId,
    status: null,
    artifacts: [],
    selectedArtifact: "",
    artifactText: "",
    activity: []
  };
}

export function applyServerEvent(state: TaskState, event: ServerEvent): TaskState {
  const activity = [...state.activity, eventToActivity(event)];
  return { ...state, activity };
}

export function eventToActivity(event: ServerEvent): ActivityItem {
  if (event.Type === "command.finished") {
    const parsed = parsePayload(event.Payload);
    return {
      id: String(event.ID),
      kind: "command",
      title: `command ${parsed.ID ?? event.ID}`,
      detail: `exit ${parsed.ExitCode ?? "?"} · touched ${formatTouched(parsed.TouchedFiles)}`
    };
  }
  if (event.Type === "check.recorded") {
    const parsed = parsePayload(event.Payload);
    return {
      id: String(event.ID),
      kind: "check",
      title: `check ${parsed.id ?? event.ID}`,
      detail: `${parsed.status ?? "unknown"}${parsed.summary ? ` · ${parsed.summary}` : ""}`
    };
  }
  return {
    id: String(event.ID),
    kind: "event",
    title: event.Type,
    detail: event.Payload
  };
}

export function addLocalActivity(state: TaskState, text: string): TaskState {
  return {
    ...state,
    activity: [
      ...state.activity,
      { id: `local-${state.activity.length + 1}`, kind: "local", title: "local", detail: text }
    ]
  };
}

function parsePayload(payload: string): Record<string, unknown> {
  try {
    return JSON.parse(payload) as Record<string, unknown>;
  } catch {
    return {};
  }
}

function formatTouched(value: unknown): string {
  if (!Array.isArray(value) || value.length === 0) {
    return "none";
  }
  return value.join(",");
}
