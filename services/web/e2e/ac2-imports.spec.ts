import { createHash } from "node:crypto";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { expect, test, type Locator, type Page } from "@playwright/test";

// PRD AC-2 (import full histories from both brokers without overwriting the source files), AC-3 (review and correct
// unmapped or invalid rows before commit) and AC-4 (re-importing a file does not duplicate transactions).
// Files are the synthetic broker fixtures (D-20); scripts/e2e.sh also checks after the run that every stored
// original is byte-identical to one of them and read-only. User C is created per browser by scripts/e2e.sh.

const FIXTURES = fileURLToPath(new URL("../../../testdata", import.meta.url));
const QT = `${FIXTURES}/questrade/synthetic_activities/input`;
const WS = `${FIXTURES}/wealthsimple/synthetic_activities/input`;

let user = { email: "", password: "" };
let committedImport = ""; // URL of user C's committed Questrade import, for the isolation check

test.describe.configure({ mode: "serial" });

test.beforeAll(({}, testInfo) => {
  user = { email: `e2e-c-${testInfo.project.name}@example.test`, password: process.env.E2E_PASSWORD_C ?? "" };
  expect(user.password, "run through make e2e").not.toBe("");
});

async function signIn(page: Page) {
  await page.goto("/");
  await page.getByLabel("Email").fill(user.email);
  await page.getByLabel("Password").fill(user.password);
  await page.getByRole("button", { name: "Sign in" }).click();
  await expect(page.getByRole("heading", { name: "Accounts" })).toBeVisible();
}

const sha256 = (path: string) => createHash("sha256").update(readFileSync(path)).digest("hex");

function counts(page: Page): Locator {
  return page.getByLabel("Row counts");
}

function rowOf(page: Page, rowNo: number): Locator {
  return page
    .getByRole("table", { name: "Import rows" })
    .getByRole("row")
    .filter({ has: page.getByRole("cell", { name: String(rowNo), exact: true }).first() })
    .first();
}

async function upload(page: Page, broker: string, file: string) {
  await page.getByRole("navigation", { name: "Main" }).getByRole("link", { name: "Imports" }).click();
  const form = page.getByRole("form", { name: "Upload export" });
  await form.getByLabel("Broker").selectOption({ label: broker });
  await form.getByLabel("File").setInputFiles(file);
  await form.getByRole("button", { name: "Upload and preview" }).click();
  return form;
}

async function openPreview(page: Page, broker: string, file: string) {
  await upload(page, broker, file);
  await expect(page.getByRole("table", { name: "Import rows" })).toBeVisible();
  // The stored original has the uploaded file's fingerprint (AC-2).
  await expect(page.getByLabel("File fingerprint")).toContainText(sha256(file));
}

async function fix(page: Page, rowNo: number, fill: (form: Locator) => Promise<unknown>, expected: string) {
  await page.getByRole("button", { name: `Fix row ${rowNo}`, exact: true }).click();
  const form = page.getByRole("form", { name: `Fix row ${rowNo}` });
  await fill(form);
  await form.getByRole("button", { name: "Save fix" }).click();
  await expect(form).toBeHidden();
  await expect(counts(page)).toHaveText(expected);
}

async function commit(page: Page, confirmText: string, resultText: string) {
  await page.getByRole("button", { name: "Commit…" }).click();
  await expect(page.getByRole("group", { name: "Confirm commit" })).toContainText(confirmText);
  await page.getByRole("button", { name: "Commit now" }).click();
  await expect(page.getByRole("status").filter({ hasText: "Committed:" })).toHaveText(resultText);
  await expect(page.locator(".tag")).toHaveText("Committed");
  await expect(page.getByRole("button", { name: "Commit…" })).toHaveCount(0);
}

test("setup: tracked accounts at both brokers", async ({ page }) => {
  await signIn(page);
  const accounts: Record<string, unknown>[] = [
    { broker: "questrade", name: "E2E QT TFSA", type: "TFSA", external_number: "90000001" },
    { broker: "questrade", name: "E2E QT FHSA", type: "FHSA", external_number: "90000002" },
    { broker: "questrade", name: "E2E QT RESP", type: "RESP" }, // no number: linked from the import preview
    { broker: "wealthsimple", name: "E2E WS TFSA", type: "TFSA", external_number: "SY12AB345CAD" },
    { broker: "wealthsimple", name: "E2E WS FHSA", type: "FHSA", external_number: "SY12AB346" },
  ];
  for (const a of accounts) {
    const res = await page.request.post("/api/accounts", { data: a });
    expect(res.status(), JSON.stringify(a)).toBe(201);
  }
});

test("AC-2/AC-3: Questrade history is reviewed, corrected and committed", async ({ page }) => {
  await signIn(page);
  await openPreview(page, "Questrade", `${QT}/activities.xlsx`);
  await expect(page.locator(".tag")).toHaveText("Needs review");
  await expect(counts(page)).toHaveText("6 errors · 3 warnings · 21 ok · 4 skipped");

  // Errors block the commit and are named; warnings are visible before commit (NFR-4).
  await expect(page.getByText("6 rows need a fix or must be excluded")).toBeVisible();
  await expect(page.getByRole("button", { name: "Commit…" })).toBeDisabled();
  await expect(rowOf(page, 17)).toContainText("no mapping for Fees and rebates / ADJ");
  await expect(rowOf(page, 8)).toContainText("differs from Net");
  await expect(rowOf(page, 13)).toContainText("broker code H000777 looks like ZZUS");

  // Untracked accounts: link one number to the existing account without a number, track the other.
  const untracked = page.getByLabel("Untracked accounts");
  await untracked.getByLabel("Existing account for 90000004").selectOption({ label: "E2E QT RESP" });
  await untracked.getByRole("button", { name: "Link 90000004" }).click();
  await expect(counts(page)).toHaveText("6 errors · 3 warnings · 24 ok · 1 skipped");
  await untracked.getByRole("button", { name: "Track this account 90000003" }).click();
  const form = page.getByRole("form", { name: "New account" });
  await expect(form.getByLabel("Account number")).toHaveValue("90000003");
  await expect(form.getByLabel("Account type")).toHaveValue("RRSP");
  await form.getByRole("button", { name: "Create account" }).click();
  await expect(counts(page)).toHaveText("6 errors · 3 warnings · 25 ok");
  await expect(page.getByLabel("Untracked accounts")).toHaveCount(0);

  // An invalid correction is refused with the reason and changes nothing.
  await page.getByRole("button", { name: "Fix row 14", exact: true }).click();
  const bad = page.getByRole("form", { name: "Fix row 14" });
  await bad.getByLabel("Symbol").fill("NOT A SYMBOL");
  await bad.getByLabel("Exchange").selectOption("TSX");
  await bad.getByRole("button", { name: "Save fix" }).click();
  await expect(bad.getByRole("alert")).toContainText("expected a listed symbol");
  await bad.getByRole("button", { name: "Cancel" }).click();
  await expect(counts(page)).toHaveText("6 errors · 3 warnings · 25 ok");

  // Unmapped actions and unknown broker codes; one code fix settles both legs of a journal pair.
  await fix(page, 17, (f) => f.getByLabel("Transaction type").selectOption("FEE"), "5 errors · 3 warnings · 26 ok");
  await fix(page, 34, (f) => f.getByLabel("Transaction type").selectOption("INTEREST"), "4 errors · 3 warnings · 27 ok");
  const symbol = (s: string) => async (f: Locator) => {
    await f.getByLabel("Symbol").fill(s);
    await f.getByLabel("Exchange").selectOption("TSX");
  };
  await fix(page, 14, symbol("OLDN"), "3 errors · 3 warnings · 28 ok");
  await fix(page, 27, symbol("SMGT"), "1 error · 3 warnings · 30 ok");
  await expect(rowOf(page, 28)).toContainText("H000555 mapped to SMGT (TSX) by your rule");

  // The last error is left out; it can be brought back and left out again.
  await page.getByRole("button", { name: "Exclude row 29", exact: true }).click();
  await expect(counts(page)).toHaveText("3 warnings · 30 ok · 1 excluded");
  await expect(page.getByRole("button", { name: "Commit…" })).toBeEnabled();
  await expect(page.getByText("33 rows will be written to the ledger, 3 of them with warnings.")).toBeVisible();

  await commit(
    page,
    "Write 33 rows to the ledger, including 3 with warnings?",
    "Committed: 33 added to the ledger, 0 already there (duplicates), 1 internal transfers linked.",
  );
  await expect(counts(page)).toHaveText("33 inserted · 1 excluded");
  await expect(rowOf(page, 14).getByRole("cell").nth(1)).toHaveText("Inserted");
  await expect(rowOf(page, 29).getByRole("cell").nth(1)).toHaveText("Excluded");
  committedImport = page.url();
});

test("AC-2/AC-3: Wealthsimple history is reviewed, corrected and committed", async ({ page }) => {
  await signIn(page);
  await openPreview(page, "Wealthsimple", `${WS}/activities.csv`);
  await expect(counts(page)).toHaveText("1 error · 3 warnings · 33 ok · 1 skipped");
  await expect(page.getByRole("button", { name: "Commit…" })).toBeDisabled();
  await expect(rowOf(page, 39)).toContainText("no mapping");
  await fix(page, 39, (f) => f.getByLabel("Transaction type").selectOption("FEE"), "3 warnings · 34 ok · 1 skipped");
  // The untracked robo account stays skipped: nothing of it reaches the ledger.
  await expect(page.getByLabel("Untracked accounts")).toBeVisible();

  // Plain tickers whose exchange the file cannot tell (D-37). The security master is shared by all users, so the
  // first browser of a run finds them unknown: the commit is refused, names the rows and writes nothing, and choosing
  // the listing settles every row of that symbol. Later browsers find exactly one listing (created by the first)
  // and the commit uses it. Either way the rows end up on the US listings.
  await page.getByRole("button", { name: "Commit…" }).click();
  await expect(page.getByRole("group", { name: "Confirm commit" })).toContainText("Write 37 rows to the ledger");
  await page.getByRole("button", { name: "Commit now" }).click();
  const refusal = page.getByRole("alert", { name: "Unresolved securities" });
  const committed = page.getByRole("status").filter({ hasText: "Committed:" });
  await expect(refusal.or(committed)).toBeVisible();
  if (await refusal.isVisible()) {
    await expect(refusal.getByRole("listitem")).toHaveText([
      "Row 34: the exchange of OLDQ is unknown; choose it",
      "Row 36: the exchange of RVSQ is unknown; choose it",
      "Row 37: the exchange of RVSQ is unknown; choose it",
    ]);
    await expect(page.locator(".tag")).toHaveText("Needs review"); // still open: nothing was written
    const us = async (f: Locator) => {
      await expect(f.getByLabel("Symbol")).not.toHaveValue("");
      await f.getByLabel("Exchange").selectOption("US");
    };
    await fix(page, 34, us, "3 warnings · 34 ok · 1 skipped");
    await expect(refusal.getByRole("listitem")).toHaveCount(2);
    await fix(page, 36, us, "3 warnings · 34 ok · 1 skipped");
    await expect(refusal).toHaveCount(0);
    await expect(rowOf(page, 37)).toContainText("RVSQ (US)");
    await commit(
      page,
      "Write 37 rows to the ledger, including 3 with warnings?",
      "Committed: 37 added to the ledger, 0 already there (duplicates), 0 internal transfers linked.",
    );
  } else {
    await expect(committed).toHaveText(
      "Committed: 37 added to the ledger, 0 already there (duplicates), 0 internal transfers linked.",
    );
    await expect(rowOf(page, 37).getByRole("cell").nth(1)).toHaveText("Inserted");
  }
  await expect(counts(page)).toHaveText("37 inserted · 1 skipped");
});

test("AC-4: the same file again is refused and points to the earlier import", async ({ page }) => {
  await signIn(page);
  const form = await upload(page, "Questrade", `${QT}/activities.xlsx`);
  await expect(form.getByRole("alert")).toContainText("this file was already imported");
  await form.getByRole("link", { name: "Open the earlier import" }).click();
  await expect(page.getByRole("heading", { name: "activities.xlsx" })).toBeVisible();
  await expect(page.locator(".tag")).toHaveText("Committed");
});

test("AC-4: an overlapping export adds only the new rows", async ({ page }) => {
  await signIn(page);
  await openPreview(page, "Questrade", `${QT}/activities_older.xlsx`);
  await expect(counts(page)).toHaveText("3 ok");
  await commit(
    page,
    "Write 3 rows to the ledger?",
    "Committed: 1 added to the ledger, 2 already there (duplicates), 0 internal transfers linked.",
  );
  await expect(counts(page)).toHaveText("1 inserted · 2 duplicates");

  await openPreview(page, "Wealthsimple", `${WS}/activities_older.csv`);
  await commit(
    page,
    "Write 3 rows to the ledger?",
    "Committed: 1 added to the ledger, 2 already there (duplicates), 0 internal transfers linked.",
  );
});

test("AC-4: a discarded file can be uploaded again and duplicates nothing", async ({ page }) => {
  await signIn(page);
  // The CSV twin of the older export: a different file with the same rows.
  await openPreview(page, "Questrade", `${QT}/activities_older.csv`);
  await page.getByRole("button", { name: "Discard…" }).click();
  await page.getByRole("button", { name: "Discard now" }).click();
  await expect(page.locator(".tag")).toHaveText("Discarded");

  await openPreview(page, "Questrade", `${QT}/activities_older.csv`);
  await commit(
    page,
    "Write 3 rows to the ledger?",
    "Committed: 0 added to the ledger, 3 already there (duplicates), 0 internal transfers linked.",
  );

  // History lists every import, newest first, with its outcome.
  await page.getByRole("navigation", { name: "Main" }).getByRole("link", { name: "Imports" }).click();
  const history = page.getByRole("table", { name: "Import history" });
  await expect(history.getByRole("row")).toHaveCount(7); // header + 6 (the refused upload made none)
  await expect(history.getByRole("row").nth(1).getByRole("cell").nth(4)).toHaveText("Committed");
  await expect(history.getByRole("row").nth(1).getByRole("cell").nth(5)).toHaveText("3 duplicates");
  await expect(history.getByRole("row").nth(2).getByRole("cell").nth(4)).toHaveText("Discarded");
});

test("FR-1: another user sees none of these imports", async ({ page }, testInfo) => {
  await page.goto("/");
  await page.getByLabel("Email").fill(`e2e-b-${testInfo.project.name}@example.test`);
  await page.getByLabel("Password").fill(process.env.E2E_PASSWORD_B ?? "");
  await page.getByRole("button", { name: "Sign in" }).click();
  await page.getByRole("navigation", { name: "Main" }).getByRole("link", { name: "Imports" }).click();
  await expect(page.getByText("No imports yet.")).toBeVisible();
  // User C's import answers exactly like a missing one, in the page and in the API.
  const id = committedImport.split("/").pop()!;
  expect(id).toMatch(/^[0-9a-f-]{36}$/);
  await page.goto(committedImport);
  await expect(page.getByRole("alert")).toHaveText("This import does not exist.");
  const res = await page.request.get(`/api/imports/${id}`);
  expect(res.status()).toBe(404);
});
