import { useCallback, useEffect, useState } from "react";
import { App } from "@/App";
import { Button } from "@/components/ui/button";
import { createDiff, deleteDiff, getDiff, NotFoundError, type SavedDiff } from "@/lib/api";
import { elapsed, repositoryName, shortRef } from "@/lib/format";
import { DeleteButton } from "./DeleteButton";
import { diffHref, HOME, Page, usePageTitle } from "./Library";
import { DiffBodySkeleton, DiffPageSkeleton, DiffShell } from "./DiffSkeleton";
import { StatusMark } from "./StatusMark";

const POLL_MS = 1000;

function Summary({ diff }: { diff: SavedDiff }) {
  return (
    <p className="text-sm text-muted-foreground">
      <span className="font-medium text-foreground">{repositoryName(diff.repository)}</span>{" "}
      <span className="font-mono break-all">{shortRef(diff.base)} → {shortRef(diff.head)}</span>
    </p>
  );
}

function Running({ diff, onDelete }: { diff: SavedDiff; onDelete: () => Promise<void> }) {
  usePageTitle("Working…");
  // The clock ticks on its own, so the page shows life between stages.
  const [now, setNow] = useState(Date.now());
  useEffect(() => {
    const timer = window.setInterval(() => setNow(Date.now()), 1000);
    return () => window.clearInterval(timer);
  }, []);
  return (
    <DiffShell home={HOME}>
      <div className="space-y-4">
        <StatusMark status="running" />
        <h1 className="text-2xl font-semibold"><span role="status" data-testid="stage">{diff.stage ?? "Working"}…</span></h1>
        <Summary diff={diff} />
        <p className="text-sm text-muted-foreground">Running for {elapsed(diff.createdAt, now)}. You can leave this page, and it will keep going.</p>
        <DeleteButton label="Stop and delete" onDelete={onDelete} />
      </div>
      {/* A preview of where the diff will appear. */}
      <div data-testid="diff-preview" className="border-t pt-8">
        <DiffBodySkeleton />
      </div>
    </DiffShell>
  );
}

function Failed({ diff, onDelete }: { diff: SavedDiff; onDelete: () => Promise<void> }) {
  usePageTitle("Diff failed");
  const [error, setError] = useState<string | null>(null);
  const retry = async () => {
    try {
      const next = await createDiff(diff.repository, diff.base, diff.head);
      window.location.hash = diffHref(next.id);
    } catch (err) {
      setError((err as Error).message);
    }
  };
  return (
    <Page>
      <StatusMark status="failed" />
      <h1 className="text-2xl font-semibold">This diff didn't finish</h1>
      <Summary diff={diff} />
      <p data-testid="diff-error" className="text-sm break-words">{diff.error}</p>
      {error && <p role="alert" className="text-sm text-remove">{error}</p>}
      <div className="flex flex-wrap items-center gap-2">
        <Button type="button" size="sm" onClick={retry}>Try again</Button>
        <DeleteButton onDelete={onDelete} />
      </div>
    </Page>
  );
}

function Missing({ message }: { message: string }) {
  usePageTitle("Not found");
  return (
    <Page>
      <h1 className="text-2xl font-semibold">Can't show this diff</h1>
      <p role="alert" className="text-muted-foreground">{message}</p>
    </Page>
  );
}

export function DiffPage({ id }: { id: string }) {
  const [diff, setDiff] = useState<SavedDiff | null>(null);
  const [gone, setGone] = useState<string | null>(null);
  const [problem, setProblem] = useState<string | null>(null);

  const refresh = useCallback(async () => {
    try {
      setDiff(await getDiff(id));
      setProblem(null);
    } catch (err) {
      if (err instanceof NotFoundError) setGone(err.message);
      else setProblem((err as Error).message);
    }
  }, [id]);

  useEffect(() => { void refresh(); }, [refresh]);

  const running = diff === null || diff.status === "running";
  useEffect(() => {
    if (!running || gone) return;
    const timer = window.setInterval(() => void refresh(), POLL_MS);
    return () => window.clearInterval(timer);
  }, [running, gone, refresh]);

  const remove = async () => {
    try {
      await deleteDiff(id);
      window.location.hash = HOME;
    } catch (err) {
      if (err instanceof NotFoundError) window.location.hash = HOME;
      else setProblem((err as Error).message);
    }
  };

  if (gone) return <Missing message={gone} />;
  if (diff === null) {
    if (problem) return <Page><p role="alert" className="text-muted-foreground">{problem}</p></Page>;
    return <DiffPageSkeleton home={HOME} />;
  }
  if (diff.status === "running") return <Running diff={diff} onDelete={remove} />;
  if (diff.status === "failed" || !diff.result) return <Failed diff={diff} onDelete={remove} />;
  return (
    <App
      model={diff.result}
      home={HOME}
      actions={<DeleteButton onDelete={remove} />}
    />
  );
}
