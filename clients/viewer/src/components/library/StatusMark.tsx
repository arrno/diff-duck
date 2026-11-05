import { CheckCircle2, CircleAlert, LoaderCircle } from "lucide-react";
import { IconMark } from "@/components/diff/visuals";
import type { DiffStatus } from "@/lib/api";

const STATUS = {
  running: { icon: LoaderCircle, label: "Running", text: "text-change", soft: "bg-change-soft" },
  done: { icon: CheckCircle2, label: "Ready", text: "text-add", soft: "bg-add-soft" },
  failed: { icon: CircleAlert, label: "Failed", text: "text-remove", soft: "bg-remove-soft" },
};

/** Status in words and an icon, so color never carries the meaning alone. */
export function StatusMark({ status }: { status: DiffStatus }) {
  const { icon, label, text, soft } = STATUS[status];
  return (
    <span className={`inline-flex shrink-0 items-center gap-2 text-xs font-semibold tracking-wide ${text}`}>
      <IconMark icon={icon} soft={soft} />
      {label}
    </span>
  );
}
