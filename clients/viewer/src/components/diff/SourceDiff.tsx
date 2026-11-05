import type { DiffNode, FileDiff } from "@/lib/model";
import { cn } from "@/lib/utils";

function collect(node: DiffNode): FileDiff[] {
  return [...(node.diff ?? []), ...(node.children ?? []).flatMap(collect)];
}

export function SourceDiff({ node }: { node: DiffNode }) {
  const diffs = collect(node).filter((d) => (d.lines ?? []).length > 0 || d.binary);
  return (
    <div data-testid="source" className="mt-3 space-y-3">
      {(node.locations ?? []).map((l) => (
        <p key={`${l.side}${l.path}${l.startLine}`} className="font-mono text-xs break-words text-muted-foreground">
          {l.side} {l.path}
          {l.startLine ? `:${l.startLine}-${l.endLine}` : ""}
        </p>
      ))}
      {diffs.length === 0 && <p className="text-xs text-muted-foreground">No changed lines to show.</p>}
      {diffs.map((d, i) => {
        const name = d.oldPath && d.newPath && d.oldPath !== d.newPath ? `${d.oldPath} → ${d.newPath}` : d.newPath || d.oldPath;
        return (
          <div key={i} className="overflow-hidden rounded-lg border bg-card">
            <p className="border-b bg-muted px-3 py-2 font-mono text-xs break-words">{name}</p>
            {d.binary && <p className="p-3 text-xs text-muted-foreground">Binary file.</p>}
            {(d.lines ?? []).length > 0 && (
              <pre tabIndex={0} aria-label={`Source diff of ${name}`} className="overflow-x-auto py-2 font-mono text-xs leading-5">
                {(d.lines ?? []).map((l, j) =>
                  l.kind === "gap" ? (
                    <div key={j} className="px-3 text-muted-foreground">…</div>
                  ) : (
                    <div
                      key={j}
                      data-kind={l.kind}
                      className={cn("w-max min-w-full px-3", l.kind === "added" && "bg-add-soft text-add", l.kind === "removed" && "bg-remove-soft text-remove")}
                    >
                      <span className="select-none">{String(l.newLine || l.oldLine || "").padStart(5, " ")} </span>
                      {l.kind === "added" ? "+" : l.kind === "removed" ? "-" : " "} {l.text ?? ""}
                    </div>
                  ),
                )}
              </pre>
            )}
          </div>
        );
      })}
    </div>
  );
}
