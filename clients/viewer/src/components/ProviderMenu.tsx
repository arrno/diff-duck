import { useRef, useState, type FormEvent } from "react";
import { Popover } from "radix-ui";
import { User } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Spinner } from "@/components/ui/spinner";
import { navIconClass } from "@/components/Navbar";
import { BadRequestError, clearKey, getSettings, saveSettings, type Settings } from "@/lib/api";
import { READING_LEVELS, type ProviderId } from "@/lib/model";

const fieldClass = "h-9 w-full rounded-md border bg-background px-3 text-sm outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-60";

/**
 * The app-wide model settings: who writes the summaries, and at what reading level. They apply
 * to the next diff started. A typed API key lives only in this form until it is sent, and is
 * cleared after saving or closing; the server never sends one back.
 */
export function ProviderMenu() {
  const [open, setOpen] = useState(false);
  const [settings, setSettings] = useState<Settings | null>(null);
  const [provider, setProvider] = useState<ProviderId>("claude-code");
  const [level, setLevel] = useState(1);
  const [key, setKey] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  // Each opening gets a number, so an answer arriving after the menu closed is ignored.
  const opening = useRef(0);
  const keyInput = useRef<HTMLInputElement>(null);

  const show = (next: Settings) => {
    setSettings(next);
    setProvider(next.provider);
    setLevel(Math.max(0, READING_LEVELS.findIndex((l) => l.id === next.readingLevel)));
  };

  const change = (next: boolean) => {
    setOpen(next);
    setKey("");
    setError("");
    setNotice("");
    // A request still in flight from the last opening no longer belongs to this form.
    setBusy(false);
    const current = ++opening.current;
    if (!next) return;
    setSettings(null);
    getSettings().then(
      (loaded) => { if (opening.current === current) show(loaded); },
      (err: Error) => { if (opening.current === current) setError(err.message); },
    );
  };

  // Runs a settings request, showing its failure in the form. Returns the server's settings, or
  // null if it failed or the menu was closed meanwhile.
  const send = async (call: () => Promise<Settings>, failureHint = "") => {
    const current = opening.current;
    setBusy(true);
    setError("");
    setNotice("");
    try {
      const next = await call();
      return opening.current === current ? next : null;
    } catch (err) {
      if (opening.current === current) {
        const message = err instanceof BadRequestError ? err.message : `Couldn't change the settings. ${(err as Error).message}`;
        setError(message + failureHint);
      }
      return null;
    } finally {
      if (opening.current === current) setBusy(false);
    }
  };

  const save = async (event: FormEvent) => {
    event.preventDefault();
    if (!settings || busy) return;
    const sent = key;
    setKey("");
    const hint = sent ? " Enter the key again." : "";
    if (await send(() => saveSettings(provider, READING_LEVELS[level].id, sent), hint)) change(false);
  };

  const forget = async () => {
    // The button disappears once the key is cleared. Moving focus first keeps it inside the
    // menu, which would otherwise close as focus fell outside it.
    keyInput.current?.focus();
    const next = await send(() => clearKey(provider));
    if (next) {
      // Only the key changed: unsaved provider and reading level choices stay as they are.
      setSettings(next);
      setNotice("Key cleared.");
    }
  };

  const pinned = settings?.pinned ?? "";
  // While pinned to a provider, the picker shows the one runs really use. Saving still sends the
  // saved provider, which applies again once the server runs without --adapter.
  const shown = settings?.pinnedProvider || provider;
  // A key belongs to the saved provider, so its field shows only when that is the one shown.
  const chosen = shown === provider ? settings?.providers.find((p) => p.id === provider) : undefined;

  return (
    <Popover.Root open={open} onOpenChange={change}>
      <Popover.Trigger asChild>
        <Button type="button" variant="ghost" size="icon" className={navIconClass} aria-label="Model settings"><User aria-hidden /></Button>
      </Popover.Trigger>
      <Popover.Portal>
        <Popover.Content align="end" sideOffset={8} aria-label="Model settings" className="z-[70] w-80 max-w-[calc(100vw-2rem)] rounded-lg border bg-background p-4 shadow-lg outline-none dark:border-foreground/30">
          <form onSubmit={save} autoComplete="off" className="space-y-3" aria-busy={!settings || busy}>
            <div className="space-y-1">
              <p className="text-sm font-semibold">Model settings</p>
              <p className="text-xs text-muted-foreground">These apply to the next diff you start.</p>
            </div>
            {!settings && !error && <p role="status" className="text-sm text-muted-foreground">Loading…</p>}
            {settings && (
              <>
                <div className="space-y-1">
                  <label htmlFor="provider" className="block text-sm font-medium">Provider</label>
                  <select
                    id="provider"
                    className={fieldClass}
                    value={shown}
                    disabled={pinned !== ""}
                    aria-describedby={pinned ? "provider-pinned" : undefined}
                    onChange={(e) => { setProvider(e.target.value as ProviderId); setKey(""); setError(""); setNotice(""); }}
                  >
                    {settings.providers.map((p) => (
                      <option key={p.id} value={p.id} disabled={!p.available}>
                        {p.available ? p.label : `${p.label} (not installed)`}
                      </option>
                    ))}
                  </select>
                  {pinned && (
                    <p id="provider-pinned" data-testid="provider-pinned" className="text-xs text-muted-foreground">
                      The server was started with <code className="break-all">--adapter {pinned}</code>, which chooses the model. The reading level still applies.
                    </p>
                  )}
                </div>
                {chosen?.needsKey && (
                  <div className="space-y-1">
                    <div className="flex items-baseline justify-between gap-2">
                      <label htmlFor="api-key" className="block text-sm font-medium">API key</label>
                      {chosen.keySaved && <span className="text-xs text-muted-foreground">Key saved</span>}
                    </div>
                    <input
                      id="api-key"
                      ref={keyInput}
                      type="password"
                      className={fieldClass}
                      value={key}
                      onChange={(e) => setKey(e.target.value)}
                      placeholder={chosen.keySaved ? "Leave blank to keep the saved key" : undefined}
                      autoComplete="off"
                      spellCheck={false}
                      // Password managers ignore autocomplete="off" on password fields; these
                      // ask them not to offer to store the key (K1).
                      data-1p-ignore="true"
                      data-lpignore="true"
                      data-bwignore="true"
                      data-form-type="other"
                    />
                    <p className="text-xs text-muted-foreground">Kept in your system keychain. It is never shown again.</p>
                    {chosen.keySaved && (
                      <Button type="button" variant="outline" size="sm" onClick={forget} disabled={busy}>Clear key</Button>
                    )}
                  </div>
                )}
                <div className="space-y-1">
                  <div className="flex items-baseline justify-between gap-2">
                    <label htmlFor="reading-level" className="block text-sm font-medium">Reading level</label>
                    <span className="text-sm text-muted-foreground" aria-hidden>{READING_LEVELS[level].label}</span>
                  </div>
                  <input
                    id="reading-level"
                    type="range"
                    min={0}
                    max={READING_LEVELS.length - 1}
                    step={1}
                    value={level}
                    onChange={(e) => setLevel(Number(e.target.value))}
                    aria-valuetext={READING_LEVELS[level].label}
                    className="w-full cursor-pointer accent-primary"
                  />
                  <div className="flex justify-between text-xs text-muted-foreground" aria-hidden>
                    <span>{READING_LEVELS[0].label}</span>
                    <span>{READING_LEVELS[READING_LEVELS.length - 1].label}</span>
                  </div>
                </div>
              </>
            )}
            {error && <p role="alert" className="text-sm break-words text-remove">{error}</p>}
            {notice && <p role="status" className="text-sm text-muted-foreground">{notice}</p>}
            <Button type="submit" size="sm" className="w-full" disabled={!settings || busy}>{busy ? <><Spinner />Saving…</> : "Save"}</Button>
          </form>
        </Popover.Content>
      </Popover.Portal>
    </Popover.Root>
  );
}
