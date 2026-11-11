// The local server's API. Every failure carries a message written for the person using the app.
import type { CompressedDiff, ProviderId, ReadingLevelId } from "./model";

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

export interface ProviderStatus {
  id: ProviderId;
  label: string;
  available: boolean;
  needsKey: boolean;
  keySaved: boolean;
}

/** The app-wide model settings. The server never returns a key, only whether one is saved. */
export interface Settings {
  provider: ProviderId;
  readingLevel: ReadingLevelId;
  /** The adapter the server was started with, which overrides the provider; empty if none. */
  pinned: string;
  /** The provider every run uses while pinned, which may differ from the saved one; empty when nothing is pinned or the pin is no provider (verification). */
  pinnedProvider: ProviderId | "";
  providers: ProviderStatus[];
}

export class NotFoundError extends Error {}
/** The server refused the request as it was sent; the message says how to fix it. */
export class BadRequestError extends Error {}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  let response: Response;
  try {
    response = await fetch(path, init);
  } catch (err) {
    if (err instanceof DOMException && err.name === "AbortError") throw err;
    throw new Error("Can't reach diffduck. Is it still running?");
  }
  if (response.status === 204) return undefined as T;
  const body = await response.json().catch(() => null);
  if (response.ok) return body as T;
  const message = body?.error ?? "Something went wrong.";
  if (response.status === 404) throw new NotFoundError(message);
  if (response.status === 400) throw new BadRequestError(message);
  throw new Error(message);
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

export const getSettings = () => request<Settings>("/api/settings");
/** Saves the settings. An empty apiKey keeps the saved key, so it is left out of the request. */
export const saveSettings = (provider: ProviderId, readingLevel: ReadingLevelId, apiKey: string) =>
  request<Settings>("/api/settings", {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(apiKey ? { provider, readingLevel, apiKey } : { provider, readingLevel }),
  });
export const clearKey = (provider: ProviderId) =>
  request<Settings>(`/api/settings/keys/${encodeURIComponent(provider)}`, { method: "DELETE" });
