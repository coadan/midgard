import { describe, expect, it } from "vitest";
import { applyServerEvent, createInitialTaskState } from "./store";

describe("task store", () => {
  it("projects mocked SSE command and check events into activity", () => {
    let state = createInitialTaskState("task_1");
    state = applyServerEvent(state, {
      ID: 1,
      TaskID: "task_1",
      Type: "command.finished",
      Payload: JSON.stringify({ ID: "cmd_1", ExitCode: 0, TouchedFiles: ["README.md"] })
    });
    state = applyServerEvent(state, {
      ID: 2,
      TaskID: "task_1",
      Type: "check.recorded",
      Payload: JSON.stringify({ id: "unit", status: "passed", summary: "go test" })
    });

    expect(state.activity).toHaveLength(2);
    expect(state.activity[0].title).toBe("command cmd_1");
    expect(state.activity[0].detail).toContain("README.md");
    expect(state.activity[1].detail).toContain("passed");
  });
});
