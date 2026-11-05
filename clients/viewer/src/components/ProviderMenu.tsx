import { useState, type FormEvent } from "react";
import { Popover } from "radix-ui";
import { User } from "lucide-react";
import { Button } from "@/components/ui/button";
import { navIconClass } from "@/components/Navbar";

const PROVIDERS = ["Anthropic", "OpenAI", "Google"];
const LEVELS = ["2nd grade", "6th grade", "High school", "College", "PhD"];
const DEFAULT_LEVEL = 1;
const fieldClass = "h-9 w-full rounded-md border bg-background px-3 text-sm outline-none focus-visible:ring-2 focus-visible:ring-ring";

/** Placeholder: nothing here is sent or stored yet. Every field lives only in this component's state. */
export function ProviderMenu() {
  const [open, setOpen] = useState(false);
  const [provider, setProvider] = useState(PROVIDERS[0]);
  const [key, setKey] = useState("");
  const [level, setLevel] = useState(DEFAULT_LEVEL);

  const change = (next: boolean) => {
    setOpen(next);
    if (!next) {
      setKey("");
      setLevel(DEFAULT_LEVEL);
    }
  };
  const save = (event: FormEvent) => {
    event.preventDefault();
    change(false);
  };

  return (
    <Popover.Root open={open} onOpenChange={change}>
      <Popover.Trigger asChild>
        <Button type="button" variant="ghost" size="icon" className={navIconClass} aria-label="Model settings"><User aria-hidden /></Button>
      </Popover.Trigger>
      <Popover.Portal>
        <Popover.Content align="end" sideOffset={8} aria-label="Model settings" className="z-[70] w-72 max-w-[calc(100vw-2rem)] rounded-lg border bg-background p-4 shadow-lg outline-none dark:border-foreground/30">
          <form onSubmit={save} className="space-y-3">
            <div className="space-y-1">
              <p className="text-sm font-semibold">Model settings</p>
              <p className="text-xs text-muted-foreground">Coming soon. Nothing here is saved yet, so don't paste a real key.</p>
            </div>
            <div className="space-y-1">
              <label htmlFor="provider" className="block text-sm font-medium">Provider</label>
              <select id="provider" className={fieldClass} value={provider} onChange={(e) => setProvider(e.target.value)}>
                {PROVIDERS.map((p) => <option key={p}>{p}</option>)}
              </select>
            </div>
            <div className="space-y-1">
              <label htmlFor="api-key" className="block text-sm font-medium">API key</label>
              <input id="api-key" type="password" className={fieldClass} value={key} onChange={(e) => setKey(e.target.value)} autoComplete="off" spellCheck={false} />
            </div>
            <div className="space-y-1">
              <div className="flex items-baseline justify-between gap-2">
                <label htmlFor="reading-level" className="block text-sm font-medium">Reading level</label>
                <span className="text-sm text-muted-foreground" aria-hidden>{LEVELS[level]}</span>
              </div>
              <input
                id="reading-level"
                type="range"
                min={0}
                max={LEVELS.length - 1}
                step={1}
                value={level}
                onChange={(e) => setLevel(Number(e.target.value))}
                aria-valuetext={LEVELS[level]}
                className="w-full cursor-pointer accent-primary"
              />
              <div className="flex justify-between text-xs text-muted-foreground" aria-hidden>
                <span>{LEVELS[0]}</span>
                <span>{LEVELS[LEVELS.length - 1]}</span>
              </div>
            </div>
            <Button type="submit" size="sm" className="w-full">Save</Button>
          </form>
        </Popover.Content>
      </Popover.Portal>
    </Popover.Root>
  );
}
