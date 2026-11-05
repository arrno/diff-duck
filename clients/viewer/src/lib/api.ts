// The local server's API. Every failure carries a message written for the person using the app.
import type { CompressedDiff } from "./model";

export type DiffStatus = "running" | "done" | "failed";

export interface SavedDiff {
  id: string;
  repository: string;
  base: string;
  head: string;
  baseSha?: string;
  headSha?: string;
  status: DiffStatus;
  stage?: string;
  error?: string;
  overview?: string;
  files: number;
  added: number;
  removed: number;
  createdAt: string;
  finishedAt?: string;
  result?: CompressedDiff;
}

export interface Commit {
  sha: string;
  subject: string;
  author: string;
  date: string;
}

export class NotFoundError extends Error {}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  let response: Response;
  try {
    response = await fetch(path, init);
  } catch (err) {
    if (err instanceof DOMException && err.name === "AbortError") throw err;
    throw new Error("Can't reach DiffDuck. Is it still running?");
  }
  if (response.status === 204) return undefined as T;
  const body = await response.json().catch(() => null);
  if (response.ok) return body as T;
  const message = body?.error ?? "Something went wrong.";
  throw response.status === 404 ? new NotFoundError(message) : new Error(message);
}

export const listDiffs = () => request<SavedDiff[]>("/api/diffs");
export const getDiff = (id: string) => request<SavedDiff>(`/api/diffs/${encodeURIComponent(id)}`);
export const deleteDiff = (id: string) => request<void>(`/api/diffs/${encodeURIComponent(id)}`, { method: "DELETE" });
export const listCommits = (repo: string, signal?: AbortSignal) =>
  request<Commit[]>(`/api/commits?${new URLSearchParams({ repo })}`, { signal });
export const createDiff = (repository: string, base: string, head: string) =>
  request<SavedDiff>("/api/diffs", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ repository, base, head }),
  });
