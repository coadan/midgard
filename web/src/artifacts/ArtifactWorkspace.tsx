import { FileCode, RefreshCw } from "lucide-react";
import type { ArtifactInfo } from "../lib/types";
import { Button } from "../components/ui/button";
import { ArtifactView } from "./ArtifactView";

type Props = {
  artifacts: ArtifactInfo[];
  selected: string;
  content: string;
  loading: boolean;
  onSelect: (path: string) => void;
  onRefresh: () => void;
};

export function ArtifactWorkspace({ artifacts, selected, content, loading, onSelect, onRefresh }: Props) {
  return (
    <section className="artifact-workspace" aria-label="Artifacts">
      <div className="pane-heading">
        <div>
          <p className="eyebrow">Artifacts</p>
          <h2>Task Files</h2>
        </div>
        <Button title="Refresh artifacts" onClick={onRefresh} variant="ghost">
          <RefreshCw size={16} />
        </Button>
      </div>
      <div className="artifact-layout">
        <nav className="artifact-list" aria-label="Artifact list">
          {artifacts.length === 0 ? (
            <div className="empty">No artifacts yet.</div>
          ) : (
            artifacts.map((artifact) => (
              <button
                className={artifact.path === selected ? "artifact-row selected" : "artifact-row"}
                key={artifact.path}
                onClick={() => onSelect(artifact.path)}
              >
                <FileCode size={15} />
                <span>{artifact.path}</span>
                <small>{artifact.size}b</small>
              </button>
            ))
          )}
        </nav>
        <ArtifactView path={selected} content={content} loading={loading} />
      </div>
    </section>
  );
}
