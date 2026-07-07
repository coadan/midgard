type Props = {
  source: string;
};

export function SafeMdxReport({ source }: Props) {
  const lines = sanitize(source).split("\n");
  return (
    <article className="safe-report">
      {lines.map((line, index) => renderLine(line, index))}
    </article>
  );
}

function sanitize(source: string): string {
  return source
    .split("\n")
    .filter((line) => !/^\s*(import|export)\s/.test(line))
    .filter((line) => !/<script/i.test(line))
    .join("\n");
}

function renderLine(line: string, index: number) {
  if (line.startsWith("# ")) {
    return <h1 key={index}>{line.slice(2)}</h1>;
  }
  if (line.startsWith("## ")) {
    return <h2 key={index}>{line.slice(3)}</h2>;
  }
  if (line.startsWith("### ")) {
    return <h3 key={index}>{line.slice(4)}</h3>;
  }
  if (line.startsWith("- ")) {
    return <li key={index}>{line.slice(2)}</li>;
  }
  if (line.trim() === "") {
    return <div className="report-gap" key={index} />;
  }
  return <p key={index}>{line}</p>;
}
