import { useEffect, useRef, useState, type FormEvent } from "react";
import { Button } from "@/components/ui/button";
import { Spinner } from "@/components/ui/spinner";
import { Dialog, DialogContent, DialogDescription, DialogTitle } from "@/components/ui/dialog";
import { createDiff, listCommits, type Commit } from "@/lib/api";
import { timeAgo } from "@/lib/format";
import { diffHref, HOME, usePageTitle } from "./Library";

const LAST_REPO_KEY = "diffduck:last-repository";
// The key used before diffduck was renamed, still read so the last repository carries over.
const LEGACY_LAST_REPO_KEY = "diff-duck:last-repository";

function remembered(): string {
  try {
    return localStorage.getItem(LAST_REPO_KEY) ?? localStorage.getItem(LEGACY_LAST_REPO_KEY) ?? "";
  } catch {
    return "";
  }
}

function remember(repository: string) {
  try {
    localStorage.setItem(LAST_REPO_KEY, repository);
  } catch {
    // Remembering is only a convenience.
  }
}

const inputClass = "h-9 w-full rounded-md border bg-background px-3 text-sm outline-none focus-visible:ring-2 focus-visible:ring-ring";

function CommitRow({ commit, base, head, pickBase, pickHead }: {
  commit: Commit;
  base: string;
  head: string;
  pickBase: () => void;
  pickHead: () => void;
}) {
  const short = commit.sha.slice(0, 8);
  const selected = base === commit.sha || head === commit.sha;
  return (
    <tr data-testid="commit" className={selected ? "bg-accent" : undefined}>
      <td className="w-12 py-2 pl-3 text-center">
        <input type="radio" name="base" aria-label={`From ${short}`} checked={base === commit.sha} onChange={pickBase} className="size-4 accent-primary" />
      </td>
      <td className="w-12 py-2 text-center">
        <input type="radio" name="head" aria-label={`To ${short}`} checked={head === commit.sha} onChange={pickHead} className="size-4 accent-primary" />
      </td>
      <td className="py-2 pr-3 pl-2">
        <p className="text-sm leading-snug font-medium break-words">{commit.subject}</p>
        <p className="mt-0.5 text-xs text-muted-foreground">
          <span className="font-mono">{short}</span> · {commit.author} · {timeAgo(commit.date)}
        </p>
      </td>
    </tr>
  );
}

export function NewDiff() {
  usePageTitle("New diff");
  const [repository, setRepository] = useState(remembered);
  const [commits, setCommits] = useState<Commit[] | null>(null);
  const [loadedFrom, setLoadedFrom] = useState("");
  const [base, setBase] = useState("");
  const [head, setHead] = useState("");
  const [loading, setLoading] = useState(false);
  const [creating, setCreating] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const pending = useRef<AbortController | null>(null);

  useEffect(() => () => pending.current?.abort(), []);

  const load = async (event: FormEvent) => {
    event.preventDefault();
    pending.current?.abort();
    const controller = new AbortController();
    pending.current = controller;
    const target = repository.trim();
    setLoading(true);
    setError(null);
    setCommits(null);
    try {
      const found = await listCommits(target, controller.signal);
      remember(target);
      setCommits(found);
      setLoadedFrom(target);
      // The latest commit is the usual question: what did it change?
      setHead(found[0]?.sha ?? "");
      setBase(found[1]?.sha ?? "");
    } catch (err) {
      if (!controller.signal.aborted) setError((err as Error).message);
    } finally {
      if (!controller.signal.aborted) setLoading(false);
    }
  };

  const create = async (event: FormEvent) => {
    event.preventDefault();
    setCreating(true);
    setError(null);
    try {
      const diff = await createDiff(loadedFrom, base.trim(), head.trim());
      window.location.hash = diffHref(diff.id);
    } catch (err) {
      setError((err as Error).message);
      setCreating(false);
    }
  };

  const baseIndex = commits?.findIndex((c) => c.sha === base) ?? -1;
  const headIndex = commits?.findIndex((c) => c.sha === head) ?? -1;
  const backwards = baseIndex >= 0 && headIndex >= 0 && baseIndex < headIndex;
  const same = base.trim() !== "" && base.trim() === head.trim();

  return (
    <Dialog open onOpenChange={(open) => { if (!open) window.location.hash = HOME; }}>
    <DialogContent className="max-w-3xl">
      <DialogTitle>New diff</DialogTitle>
      <DialogDescription>Point at a Git repo, then pick two commits.</DialogDescription>

      <form onSubmit={load} className="space-y-2">
        <label htmlFor="repository" className="block text-sm font-medium">Git repo</label>
        <div className="flex flex-col gap-2 sm:flex-row">
          <input
            id="repository"
            className={inputClass}
            value={repository}
            onChange={(e) => setRepository(e.target.value)}
            placeholder="~/projects/my-app or https://github.com/org/repo"
            spellCheck={false}
            autoCapitalize="off"
            autoCorrect="off"
            required
            autoFocus
          />
          <Button type="submit" disabled={loading} className="shrink-0">{loading ? <><Spinner />Loading…</> : "Load commits"}</Button>
        </div>
      </form>

      {error && <p role="alert" className="text-sm break-words text-remove">{error}</p>}

      {commits && commits.length === 0 && <p className="text-muted-foreground">This repo has no commits yet.</p>}

      {commits && commits.length > 0 && (
        <form onSubmit={create} className="space-y-4">
                    {/* Scrolls on its own, so a long history never pushes the choice and the button away. */}
          <div className="max-h-64 overflow-auto rounded-lg border">
            <table className="w-full border-collapse">
              <thead className="sticky top-0 z-10 bg-background text-left text-xs text-muted-foreground shadow-[0_1px_0_var(--border)]">
                <tr>
                  <th scope="col" className="py-2 pl-3 text-center font-medium">From</th>
                  <th scope="col" className="py-2 text-center font-medium">To</th>
                  <th scope="col" className="py-2 pr-3 pl-2 font-medium">Commit</th>
                </tr>
              </thead>
              <tbody className="divide-y">
                {commits.map((c) => (
                  <CommitRow key={c.sha} commit={c} base={base} head={head} pickBase={() => setBase(c.sha)} pickHead={() => setHead(c.sha)} />
                ))}
              </tbody>
            </table>
          </div>

          <div className="grid gap-3 sm:grid-cols-2">
            <div className="space-y-1">
              <label htmlFor="base" className="block text-sm font-medium">From</label>
              <input id="base" className={`${inputClass} font-mono`} value={base} onChange={(e) => setBase(e.target.value)} spellCheck={false} required />
            </div>
            <div className="space-y-1">
              <label htmlFor="head" className="block text-sm font-medium">To</label>
              <input id="head" className={`${inputClass} font-mono`} value={head} onChange={(e) => setHead(e.target.value)} spellCheck={false} required />
            </div>
          </div>
          <p className="text-xs text-muted-foreground">Or type any branch, tag, or commit.</p>

          {backwards && <p role="status" className="text-sm text-warn">From is newer than To, so the changes will read backwards.</p>}
          {same && <p role="status" className="text-sm text-warn">From and To are the same, so there will be nothing to show.</p>}

          <div className="flex flex-wrap items-center gap-3">
            <Button type="submit" disabled={creating || !base.trim() || !head.trim()}>{creating ? <><Spinner />Starting…</> : "Create diff"}</Button>
            <p className="text-xs text-muted-foreground">Uses your model and can take a few minutes. It keeps running if you leave this page.</p>
          </div>
        </form>
      )}
    </DialogContent>
    </Dialog>
  );
}
