import type { ServerEvent, StreamEvent } from "../lib/types";

export function parseSSEChunk(chunk: string): StreamEvent[] {
  return chunk
    .split("\n\n")
    .map((block) => block.trimEnd())
    .filter(Boolean)
    .map((block) => {
      const event: StreamEvent = { id: "", type: "message", data: "" };
      const data: string[] = [];
      for (const line of block.split("\n")) {
        if (line.startsWith("id:")) {
          event.id = line.slice(3).trim();
        } else if (line.startsWith("event:")) {
          event.type = line.slice(6).trim();
        } else if (line.startsWith("data:")) {
          data.push(line.slice(5).trimStart());
        }
      }
      event.data = data.join("\n");
      return event;
    });
}

export function streamToServerEvent(taskId: string, event: StreamEvent): ServerEvent {
  return {
    ID: Number(event.id || 0),
    TaskID: taskId,
    Type: event.type,
    Payload: event.data
  };
}
