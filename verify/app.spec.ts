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

test("the person menu opens, and keeps nothing yet", async ({ page }) => {
  const requests: string[] = [];
  page.on("request", (r) => { if (r.method() !== "GET") requests.push(r.url()); });
  await page.goto(app);
  await page.getByRole("button", { name: "Model settings" }).click();
  const menu = page.getByRole("dialog", { name: "Model settings" });
  await expect(menu.getByLabel("Provider")).toHaveValue("Anthropic");
  await menu.getByLabel("Provider").selectOption("OpenAI");
  await menu.getByLabel("API key").fill("not-a-real-key");
  await noViolations(page);
  await menu.getByRole("button", { name: "Save" }).click();
  await expect(menu).toHaveCount(0);
  expect(requests).toEqual([]);
  const stored = await page.evaluate(() => JSON.stringify({ ...localStorage }) + JSON.stringify({ ...sessionStorage }));
  expect(stored).not.toContain("not-a-real-key");
  await page.getByRole("button", { name: "Model settings" }).click();
  await expect(menu.getByLabel("API key")).toHaveValue("");
  await page.keyboard.press("Escape");
  await expect(menu).toHaveCount(0);
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
