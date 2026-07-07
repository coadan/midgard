export type TaskStatus = {
  Task: {
    ID: string;
    State: string;
    Objective: string;
  };
  Worktrees: WorktreeStatus[] | null;
  NextAction: string;
};

export type WorktreeStatus = {
  RepoID: string;
  Path: string;
  Dirty: boolean;
};

export type ArtifactInfo = {
  path: string;
  size: number;
};

export type ServerEvent = {
  ID: number;
  TaskID: string;
  Type: string;
  Payload: string;
};

export type StreamEvent = {
  id: string;
  type: string;
  data: string;
};
