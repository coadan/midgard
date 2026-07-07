import { SafeMdxReport } from "../report/SafeMdxReport";

type Props = {
  path: string;
  content: string;
  loading: boolean;
};

export function ArtifactView({ path, content, loading }: Props) {
  if (!path) {
    return <div className="artifact-view empty">Select an artifact.</div>;
  }
  if (loading) {
    return <div className="artifact-view empty">Loading {path}</div>;
  }
  if (path.endsWith(".mdx") || path.endsWith(".md")) {
    return (
      <div className="artifact-view report">
        <SafeMdxReport source={content} />
      </div>
    );
  }
  return (
    <pre className="artifact-view code" tabIndex={0}>
      {content}
    </pre>
  );
}
