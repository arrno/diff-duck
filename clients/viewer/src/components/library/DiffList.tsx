import { useCallback, useEffect, useState } from "react";
import { Card } from "@/components/ui/card";
import { LineStats } from "@/components/diff/visuals";
import { deleteDiff, listDiffs, type SavedDiff } from "@/lib/api";
import { elapsed, repositoryName, shortRef, timeAgo } from "@/lib/format";
import { DeleteButton } from "./DeleteButton";
import { diffHref, NewDiffLink, Page, usePageTitle } from "./Library";
import { StatusMark } from "./StatusMark";

const POLL_MS = 1500;

function headline(diff: SavedDiff): string {
  if (diff.status === "running") return `${diff.stage ?? "Working"}…`;
  if (diff.status === "failed") return diff.error ?? "This diff failed.";
  return diff.overview || "No summary for this diff.";
}

function DiffRow({ diff, onDelete }: { diff: SavedDiff; onDelete: () => Promise<void> }) {
  return (
    <li data-testid="diff-row">
      <Card className="gap-3 rounded-lg p-4 shadow-none transition-colors hover:bg-muted/50">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <StatusMark status={diff.status} />
          <span className="text-xs text-muted-foreground">
            {diff.status === "running" ? elapsed(diff.createdAt) : timeAgo(diff.createdAt)}
          </span>
        </div>
        <a href={diffHref(diff.id)} className="text-base leading-snug font-semibold hover:underline">{headline(diff)}</a>
        <div className="flex flex-wrap items-center justify-between gap-x-4 gap-y-2">
          <p className="flex min-w-0 flex-wrap items-center gap-x-3 gap-y-1 text-xs text-muted-foreground">
            <span className="font-medium text-foreground">{repositoryName(diff.repository)}</span>
            <span className="font-mono break-all">{shortRef(diff.base)} → {shortRef(diff.head)}</span>
            {diff.status === "done" && <span>{diff.files} files</span>}
            {diff.status === "done" && <LineStats added={diff.added} removed={diff.removed} />}
          </p>
          <DeleteButton label={diff.status === "running" ? "Stop and delete" : "Delete"} onDelete={onDelete} />
        </div>
      </Card>
    </li>
  );
}

export function DiffList() {
  usePageTitle("Your diffs");
  const [diffs, setDiffs] = useState<SavedDiff[] | null>(null);
  const [error, setError] = useState<string | null>(null);

  const refresh = useCallback(async () => {
    try {
      setDiffs(await listDiffs());
      setError(null);
    } catch (err) {
      setError((err as Error).message);
    }
  }, []);

  useEffect(() => { void refresh(); }, [refresh]);

  const anyRunning = diffs?.some((d) => d.status === "running") ?? false;
  useEffect(() => {
    if (!anyRunning) return;
    const timer = window.setInterval(() => void refresh(), POLL_MS);
    return () => window.clearInterval(timer);
  }, [anyRunning, refresh]);

  const remove = async (id: string) => {
    try {
      await deleteDiff(id);
    } catch (err) {
      setError((err as Error).message);
    }
    await refresh();
  };

  return (
    <Page>
      <div className="flex flex-wrap items-center justify-between gap-3">
        <h1 className="text-2xl font-semibold">Your diffs</h1>
        {/* An empty list has its own invitation to create the first one. */}
        {diffs && diffs.length > 0 && <NewDiffLink />}
      </div>
      {error && <p role="alert" className="text-sm text-remove">{error}</p>}
      {diffs === null && !error && <p role="status" className="text-muted-foreground">Loading…</p>}
      {diffs?.length === 0 && (
        <div data-testid="no-diffs" className="space-y-4 py-12 text-center">
          <p className="text-lg font-medium">No diffs yet</p>
          <p className="text-muted-foreground">Point DiffDuck at a Git repo and pick two commits.</p>
          <div className="flex justify-center"><NewDiffLink /></div>
        </div>
      )}
      {diffs && diffs.length > 0 && (
        <ul className="space-y-3">
          {diffs.map((d) => <DiffRow key={d.id} diff={d} onDelete={() => remove(d.id)} />)}
        </ul>
      )}
    </Page>
  );
}
