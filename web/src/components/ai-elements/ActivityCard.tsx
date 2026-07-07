import { CheckCircle2, Circle, Terminal } from "lucide-react";
import type { ActivityItem } from "../../task-store/store";

type Props = {
  item: ActivityItem;
};

export function ActivityCard({ item }: Props) {
  const Icon = item.kind === "command" ? Terminal : item.kind === "check" ? CheckCircle2 : Circle;
  return (
    <article className={`activity-card ${item.kind}`}>
      <Icon size={16} />
      <div>
        <h3>{item.title}</h3>
        <p>{item.detail}</p>
      </div>
    </article>
  );
}
