import type { ArtifactInfo, ServerEvent, TaskStatus } from "./types";

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

async function fetchJSON<T>(path: string): Promise<T> {
  const response = await fetch(path);
  if (!response.ok) {
    throw new Error(`request ${response.status}`);
  }
  return response.json() as Promise<T>;
}
