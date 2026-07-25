import type { ArtifactInfo, ServerEvent, TaskStatus } from "./types";

export async function createTask(taskId: string, objective: string): Promise<void> {
  await fetchJSON("/api/tasks", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ id: taskId, objective, repos: [] })
  });
}

export async function runTask(taskId: string): Promise<void> {
  await fetchJSON(`/api/tasks/${encodeURIComponent(taskId)}/run`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: "{}"
  });
}

export async function fetchTask(taskId: string): Promise<TaskStatus> {
  return fetchJSON(`/api/tasks/${encodeURIComponent(taskId)}`);
}

export async function fetchArtifacts(taskId: string): Promise<ArtifactInfo[]> {
  return fetchJSON(`/api/artifacts?task=${encodeURIComponent(taskId)}`);
}

export async function fetchArtifact(taskId: string, path: string): Promise<string> {
  const response = await fetch(`/api/artifacts/${encodeURIComponent(taskId)}/${path}`);
  if (!response.ok) {
    throw new Error(`artifact ${response.status}`);
  }
  return response.text();
}

export async function fetchEvents(taskId: string): Promise<ServerEvent[]> {
  return fetchJSON(`/api/events?task=${encodeURIComponent(taskId)}`);
}

async function fetchJSON<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await fetch(path, init);
  if (!response.ok) {
    let message = `request ${response.status}`;
    try {
      const body = (await response.json()) as { error?: string };
      if (body.error) {
        message = body.error;
      }
    } catch {
      // Keep the bounded status fallback when the response is not JSON.
    }
    throw new Error(message);
  }
  return response.json() as Promise<T>;
}
