// The local app end to end: a real server, a real SQLite file, a real Git repository, and a
// scripted model. Runs in order because the tests share one database.
import { test, expect, Page } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";

const app = process.env.APP_URL as string;
const fixture = process.env.APP_FIXTURE as string;

test.describe.configure({ mode: "serial" });

async function watchErrors(page: Page) {
  const errors: string[] = [];
  page.on("console", (m) => { if (m.type() === "error") errors.push(m.text()); });
  page.on("pageerror", (e) => errors.push(String(e)));
  return errors;
}

async function noViolations(page: Page) {
  const results = await new AxeBuilder({ page }).analyze();
  expect(results.violations.map((v) => `${v.id}: ${v.nodes[0]?.html}`)).toEqual([]);
}

async function loadCommits(page: Page) {
  await page.goto(`${app}#/new`);
  await page.getByLabel("Git repo").fill(fixture);
  await page.getByRole("button", { name: "Load commits" }).click();
  await expect(page.getByTestId("commit").first()).toBeVisible();
}

test("starts with an invitation to create a diff", async ({ page }) => {
  const errors = await watchErrors(page);
  await page.goto(app);
  await expect(page).toHaveTitle("Your diffs · DiffDuck");
  await expect(page.getByTestId("no-diffs")).toBeVisible();
  await noViolations(page);
  expect(errors).toEqual([]);
});

test("moving between pages survives browsers whose scrollTo returns a promise", async ({ page }) => {
  const errors = await watchErrors(page);
  await page.addInitScript(() => {
    const scrollTo = window.scrollTo.bind(window);
    window.scrollTo = ((...args: [number, number]) => { scrollTo(...args); return Promise.resolve(); }) as typeof window.scrollTo;
  });
  await page.goto(app);
  await page.getByRole("link", { name: "New diff" }).first().click();
  await expect(page.getByRole("dialog")).toBeVisible();
  await page.goBack();
  await expect(page.getByRole("heading", { name: "Your diffs" })).toBeVisible();
  expect(errors).toEqual([]);
});

test("new diff is a modal over the list, and closing it returns there", async ({ page }) => {
  await page.goto(app);
  await page.getByRole("link", { name: "New diff" }).first().click();
  const dialog = page.getByRole("dialog");
  await expect(dialog).toContainText("New diff");
  await expect(dialog.getByLabel("Git repo")).toBeFocused();
  await expect(page).toHaveTitle("New diff · DiffDuck");
  await noViolations(page);
  await page.keyboard.press("Escape");
  await expect(dialog).toHaveCount(0);
  await expect(page.getByRole("heading", { name: "Your diffs" })).toBeVisible();
  await expect(page).toHaveTitle("Your diffs · DiffDuck");
});

test("short and long pages reserve the same scrollbar space", async ({ page }) => {
  await page.goto(app);
  expect(await page.evaluate(() => getComputedStyle(document.documentElement).scrollbarGutter)).toBe("stable");
});

for (const theme of ["light", "dark"] as const) {
  test(`model settings opened in the ${theme} theme pass an accessibility check`, async ({ page }) => {
    await page.emulateMedia({ colorScheme: theme });
    await page.goto(app);
    await page.getByRole("button", { name: "Model settings" }).click();
    await expect(page.getByRole("dialog", { name: "Model settings" }).getByLabel("Reading level")).toBeVisible();
    await noViolations(page);
  });
}

test("a server started with --adapter pins the provider, and the reading level persists", async ({ page }) => {
  const sent: string[] = [];
  page.on("request", (r) => { if (r.method() === "PUT") sent.push(r.postData() ?? ""); });
  await page.goto(app);
  const trigger = page.getByRole("button", { name: "Model settings" });
  await trigger.click();
  const menu = page.getByRole("dialog", { name: "Model settings" });
  const provider = menu.getByLabel("Provider");
  await expect(provider).toBeDisabled();
  await expect(menu.getByTestId("provider-pinned")).toContainText("--adapter scripted:normal");
  await expect(provider).toHaveAccessibleDescription(/--adapter scripted:normal/);
  await expect(provider.locator("option")).toHaveCount(4);
  await expect(provider.locator("option").first()).toContainText("Claude Code");
  const level = menu.getByLabel("Reading level");
  await expect(level).toHaveAttribute("aria-valuetext", "6th grade");

  // Keyboard only: into the slider, two steps up, then Save.
  await level.focus();
  await page.keyboard.press("ArrowRight");
  await page.keyboard.press("ArrowRight");
  await expect(level).toHaveAttribute("aria-valuetext", "College");
  await menu.getByRole("button", { name: "Save" }).focus();
  await page.keyboard.press("Enter");
  await expect(menu).toHaveCount(0);
  await expect(trigger).toBeFocused();
  expect(sent).toEqual([JSON.stringify({ provider: "claude-code", readingLevel: "college" })]);

  await page.reload();
  await page.getByRole("button", { name: "Model settings" }).click();
  await expect(menu.getByLabel("Reading level")).toHaveAttribute("aria-valuetext", "College");
  await page.keyboard.press("Escape");
  await expect(menu).toHaveCount(0);
});

test("an API provider takes a key that is never kept in the page", async ({ page }) => {
  // The pinned server cannot reach this flow without the real keychain, so the API is stubbed.
  const key = "sk-test-not-a-real-key";
  const providers = (keySaved: boolean) => [
    { id: "claude-code", label: "Claude Code", available: true, needsKey: false, keySaved: false },
    { id: "codex", label: "Codex", available: false, needsKey: false, keySaved: false },
    { id: "anthropic-api", label: "Anthropic API", available: true, needsKey: true, keySaved },
    { id: "openai-api", label: "OpenAI API", available: true, needsKey: true, keySaved: false },
  ];
  const settings = (keySaved: boolean) => ({ provider: "anthropic-api", readingLevel: "6th-grade", pinned: "", providers: providers(keySaved) });
  const calls: string[] = [];
  await page.route((url) => url.pathname.startsWith("/api/settings"), async (route) => {
    const r = route.request();
    calls.push(`${r.method()} ${new URL(r.url()).pathname} ${r.postData() ?? ""}`.trim());
    if (r.method() === "GET") return route.fulfill({ json: settings(true) });
    if (r.method() === "DELETE") return route.fulfill({ json: settings(false) });
    return route.fulfill({ status: 400, json: { error: "invalid settings: the API key may hold only printable ASCII without spaces" } });
  });
  await page.goto(app);
  await page.getByRole("button", { name: "Model settings" }).click();
  const menu = page.getByRole("dialog", { name: "Model settings" });
  const provider = menu.getByLabel("Provider");
  await expect(provider).toBeEnabled();
  await expect(provider).toHaveValue("anthropic-api");
  await expect(provider.locator("option", { hasText: "Codex" })).toHaveText("Codex (not installed)");
  await expect(provider.locator("option", { hasText: "Codex" })).toBeDisabled();
  await expect(menu.getByText("Key saved")).toBeVisible();
  const keyField = menu.getByLabel("API key");
  await expect(keyField).toHaveAttribute("placeholder", "Leave blank to keep the saved key");
  await expect(keyField).toHaveAttribute("type", "password");
  await noViolations(page);

  await menu.getByRole("button", { name: "Clear key" }).click();
  await expect(menu.getByText("Key saved")).toHaveCount(0);
  await expect(menu.getByRole("button", { name: "Clear key" })).toHaveCount(0);
  await expect(keyField).toBeFocused();
  await expect(keyField).not.toHaveAttribute("placeholder", /.+/);

  await keyField.fill(key);
  await menu.getByRole("button", { name: "Save" }).click();
  await expect(menu.getByRole("alert")).toHaveText(/printable ASCII/);
  await expect(menu).toBeVisible();
  await expect(keyField).toHaveValue("");
  expect(calls).toEqual([
    "GET /api/settings",
    "DELETE /api/settings/keys/anthropic-api",
    `PUT /api/settings ${JSON.stringify({ provider: "anthropic-api", readingLevel: "6th-grade", apiKey: key })}`,
  ]);

  // A CLI provider has no key field.
  await provider.selectOption("claude-code");
  await expect(menu.getByLabel("API key")).toHaveCount(0);
  await page.keyboard.press("Escape");
  await expect(menu).toHaveCount(0);
  const stored = await page.evaluate(() => JSON.stringify({ ...localStorage }) + JSON.stringify({ ...sessionStorage }) + document.body.innerHTML);
  expect(stored).not.toContain(key);
});

test("a path that is not a repo says so", async ({ page }) => {
  await page.goto(`${app}#/new`);
  await page.getByLabel("Git repo").fill("/no/such/place");
  await page.getByRole("button", { name: "Load commits" }).click();
  await expect(page.getByRole("alert")).toContainText("not a directory");
  await expect(page.getByTestId("commit")).toHaveCount(0);
});

test("picks commits, creates a diff, and opens it", async ({ page }) => {
  const errors = await watchErrors(page);
  await loadCommits(page);
  await noViolations(page);

  // The latest commit against the one before it is already chosen.
  const rows = page.getByTestId("commit");
  await expect(rows.nth(0).getByRole("radio", { name: /^To / })).toBeChecked();
  await expect(rows.nth(1).getByRole("radio", { name: /^From / })).toBeChecked();

  // Choosing them the wrong way round is called out before anything is spent.
  await rows.nth(0).getByRole("radio", { name: /^From / }).click();
  await rows.nth(1).getByRole("radio", { name: /^To / }).click();
  await expect(page.getByText("will read backwards")).toBeVisible();
  await rows.nth(1).getByRole("radio", { name: /^From / }).click();
  await rows.nth(0).getByRole("radio", { name: /^To / }).click();
  await expect(page.getByText("will read backwards")).toHaveCount(0);

  await page.getByRole("button", { name: "Create diff" }).click();
  await expect(page.getByTestId("overview")).toHaveText("Scripted overview of the change.");
  await expect(page).toHaveTitle(/ · DiffDuck$/);
  await expect(page.getByTestId("outcomes")).toBeVisible();
  // The delete button sits with the tree's other controls, and the name leads home.
  const controls = page.locator("#changes").getByRole("button", { name: "Expand all" }).locator("..");
  await expect(controls.getByRole("button", { name: "Delete" })).toBeVisible();
  await expect(page.getByRole("link", { name: "All diffs" })).toHaveCount(0);
  await noViolations(page);
  expect(errors).toEqual([]);
});

test("the saved diff is listed and survives a reload", async ({ page }) => {
  await page.goto(app);
  const row = page.getByTestId("diff-row");
  await expect(row).toHaveCount(1);
  await expect(row).toContainText("Ready");
  await expect(row).toContainText("Scripted overview of the change.");
  // The bar is the same in every view; creating a diff belongs to the list.
  await expect(page.getByRole("banner").getByRole("link", { name: "New diff" })).toHaveCount(0);
  await expect(page.getByRole("main").getByRole("link", { name: "New diff" })).toBeVisible();
  await noViolations(page);
  await page.reload();
  await row.getByRole("link").click();
  await expect(page.getByTestId("overview")).toBeVisible();
  // The address names the diff, so reloading or bookmarking comes back to it.
  await page.reload();
  await expect(page.getByTestId("overview")).toBeVisible();
});

test("a bad commit fails visibly and can be retried or deleted", async ({ page }) => {
  await loadCommits(page);
  await page.getByLabel("From", { exact: true }).fill("no-such-commit");
  await page.getByRole("button", { name: "Create diff" }).click();
  await expect(page.getByRole("heading", { name: "This diff didn't finish" })).toBeVisible();
  await expect(page.getByTestId("diff-error")).toContainText("unknown revision");
  await noViolations(page);

  await page.getByRole("button", { name: "Try again" }).click();
  await expect(page.getByTestId("diff-error")).toContainText("unknown revision");

  await page.goto(app);
  const failed = page.getByTestId("diff-row").filter({ hasText: "Failed" });
  await expect(failed).toHaveCount(2);
  for (const _ of [0, 1]) {
    await failed.first().getByRole("button", { name: "Delete" }).click();
    await page.getByRole("alertdialog").getByRole("button", { name: "Yes, delete" }).click();
  }
  await expect(failed).toHaveCount(0);
});

test("deleting asks first, and a deleted diff is really gone", async ({ page }) => {
  await page.goto(app);
  const row = page.getByTestId("diff-row");
  await row.getByRole("link").click();
  await expect(page.getByTestId("overview")).toBeVisible();
  const address = page.url();

  await page.getByRole("button", { name: "Delete" }).click();
  await expect(page.getByRole("alertdialog")).toContainText("Delete this diff?");
  await noViolations(page);
  await page.getByRole("button", { name: "Keep" }).click();
  await expect(page.getByRole("alertdialog")).toHaveCount(0);
  await expect(page.getByTestId("overview")).toBeVisible();

  await page.getByRole("button", { name: "Delete" }).click();
  await page.getByRole("button", { name: "Yes, delete" }).click();
  await expect(page.getByTestId("no-diffs")).toBeVisible();

  await page.goto(address);
  await page.reload();
  await expect(page.getByRole("heading", { name: "Can't show this diff" })).toBeVisible();
  await page.getByRole("link", { name: "DiffDuck" }).click();
  await expect(page.getByRole("heading", { name: "Your diffs" })).toBeVisible();
});

test("usable at 360px", async ({ page }) => {
  await page.setViewportSize({ width: 360, height: 740 });
  await loadCommits(page);
  const wide = () => page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
  expect(await wide()).toBeLessThanOrEqual(0);
  await page.getByRole("button", { name: "Create diff" }).click();
  await expect(page.getByTestId("overview")).toBeVisible();
  expect(await wide()).toBeLessThanOrEqual(0);
  await page.goto(app);
  await expect(page.getByTestId("diff-row")).toHaveCount(1);
  expect(await wide()).toBeLessThanOrEqual(0);
});

test("another site's page cannot read or change saved diffs", async ({ page }) => {
  await page.goto(app);
  // The diff left by the previous test. Waiting for it also proves the list has loaded.
  await expect(page.getByTestId("diff-row")).toHaveCount(1);
  // A real browser making a real cross-origin attempt: localhost and 127.0.0.1 are different origins.
  const other = app.replace("127.0.0.1", "localhost");
  await page.goto(other);
  const outcome = await page.evaluate(async ({ target, valid }) => {
    const read = await fetch(`${target}api/diffs`).then(() => "read", () => "blocked");
    const write = await fetch(`${target}api/diffs`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(valid) }).then(() => "wrote", () => "blocked");
    return { read, write };
  }, { target: app, valid: { repository: fixture, base: "base", head: "head" } });
  expect(outcome).toEqual({ read: "blocked", write: "blocked" });
  await page.goto(app);
  // A valid request, so only the refusal can explain no new diff appearing.
  await expect(page.getByTestId("diff-row")).toHaveCount(1);
});
