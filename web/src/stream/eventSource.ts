import type { ServerEvent } from "../lib/types";
import { streamToServerEvent } from "./events";

export function connectTaskEvents(taskId: string, onEvent: (event: ServerEvent) => void): () => void {
  const source = new EventSource(`/api/events/stream?task=${encodeURIComponent(taskId)}`);
  source.onmessage = (message) => {
    onEvent(streamToServerEvent(taskId, { id: message.lastEventId, type: "message", data: message.data }));
  };
  source.addEventListener("command.finished", (message) => {
    onEvent(streamToServerEvent(taskId, { id: message.lastEventId, type: "command.finished", data: message.data }));
  });
  source.addEventListener("check.recorded", (message) => {
    onEvent(streamToServerEvent(taskId, { id: message.lastEventId, type: "check.recorded", data: message.data }));
  });
  return () => source.close();
}
