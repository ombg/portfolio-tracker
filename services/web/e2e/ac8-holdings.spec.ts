import { fileURLToPath } from "node:url";
import { expect, test, type Locator, type Page } from "@playwright/test";

// PRD AC-8: view a security's combined exposure and its account-level holdings (FR-7, FR-8, D-38). User D imports
// testdata/questrade/synthetic_holdings, whose README shows the expected numbers worked by hand (D-20). Each table
// row is compared cell by cell so one column cannot hide a wrong value in another (specs/testing.md).

const FILE = fileURLToPath(new URL("../../../testdata/questrade/synthetic_holdings/activities.csv", import.meta.url));

let user = { email: "", password: "" };

test.describe.configure({ mode: "serial" });

test.beforeAll(({}, testInfo) => {
  user = { email: `e2e-d-${testInfo.project.name}@example.test`, password: process.env.E2E_PASSWORD_D ?? "" };
  expect(user.password, "run through make e2e").not.toBe("");
});

async function signIn(page: Page) {
  await page.goto("/");
  await page.getByLabel("Email").fill(user.email);
  await page.getByLabel("Password").fill(user.password);
  await page.getByRole("button", { name: "Sign in" }).click();
  await expect(page.getByRole("heading", { name: "Accounts" })).toBeVisible();
}

async function openHoldings(page: Page) {
  await page.getByRole("navigation", { name: "Main" }).getByRole("link", { name: "Holdings" }).click();
  await expect(page.getByRole("heading", { name: "Holdings" })).toBeVisible();
}

function bodyRows(table: Locator): Locator {
  return table.locator("tbody tr");
}

async function expectRows(table: Locator, rows: (string | RegExp)[][]) {
  await expect(bodyRows(table)).toHaveCount(rows.length);
  for (let i = 0; i < rows.length; i++) {
    await expect(bodyRows(table).nth(i).getByRole("cell")).toHaveText(rows[i]);
  }
}

const QZHG = "QZHG (TSX)SYNTHETIC HOLDINGS ETF";
const ZZHU = /^ZZHU \(US\)ZEDCO HOLDINGS SYNTHETIC INC/;

test("setup: three accounts, one of them managed, and the synthetic history committed", async ({ page }) => {
  await signIn(page);
  for (const a of [
    { broker: "questrade", name: "E2E D TFSA", type: "TFSA", external_number: "91000001" },
    { broker: "questrade", name: "E2E D FHSA", type: "FHSA", external_number: "91000002" },
    { broker: "questrade", name: "E2E D Managed RRSP", type: "RRSP", external_number: "91000003", management: "robo" },
  ]) {
    const res = await page.request.post("/api/accounts", { data: a });
    expect(res.status(), JSON.stringify(a)).toBe(201);
  }

  // Before any import the page says so instead of showing zeros (NFR-4).
  await openHoldings(page);
  await expect(page.getByText("Nothing in the ledger yet")).toBeVisible();

  await page.getByRole("navigation", { name: "Main" }).getByRole("link", { name: "Imports" }).click();
  const form = page.getByRole("form", { name: "Upload export" });
  await form.getByLabel("Broker").selectOption({ label: "Questrade" });
  await form.getByLabel("File").setInputFiles(FILE);
  await form.getByRole("button", { name: "Upload and preview" }).click();
  await expect(page.getByLabel("Row counts")).toHaveText("11 ok");
  await expect(page.locator(".tag")).toHaveText("Ready to commit");
  await page.getByRole("button", { name: "Commit…" }).click();
  await expect(page.getByRole("group", { name: "Confirm commit" })).toContainText("Write 11 rows to the ledger?");
  await page.getByRole("button", { name: "Commit now" }).click();
  await expect(page.getByRole("status").filter({ hasText: "Committed:" })).toHaveText(
    "Committed: 11 added to the ledger, 0 already there (duplicates), 0 internal transfers linked.",
  );
});

test("AC-8: holdings by account, with totals that say what they leave out", async ({ page }) => {
  await signIn(page);
  await openHoldings(page);

  const totals = page.getByLabel("Totals");
  await expect(totals).toContainText("Book value1,257.97 CADexcludes 1 position with an unknown cost");
  // The recorded provider data has no price for these synthetic tickers: left out and counted, never 0.
  await expect(totals).toContainText("Market valueNo priceexcludes 4 positions without a value");
  await expect(totals).toContainText("Realized gain73.07 CAD");
  await expect(totals).toContainText("Income received6.00 CAD");
  await expect(totals).toContainText("Cash2,181.10 CAD0.00 USD");

  const header = ["Security", "Quantity", "Average cost", "Book value", "Book value (CAD)", "Market value (CAD)",
    "Unrealized (CAD)", "Weight", "Realized gain (CAD)", "Income (CAD)"];
  const tfsa = page.getByRole("table", { name: "Holdings in E2E D TFSA" });
  await expect(tfsa.locator("thead th")).toHaveText(header);
  await expectRows(tfsa, [
    [QZHG, "60", "10.0495 CAD", "602.97 CAD", "602.97", "No price", "–", "–", "73.07", "6.00"],
    [ZZHU, "5", "20.00 USD", "100.00 USD", "Unknown", "No price", "–", "–", "0.00", "0.00"],
  ]);
  // A USD purchase without a broker rate: the CAD value is unknown and says why, not 0.
  await expect(bodyRows(tfsa).nth(1).locator(".tag.flag")).toHaveText("FX rate missing");
  await expect(page.getByLabel("Cash in E2E D TFSA")).toHaveText("Cash: 1,336.10 CAD · 0.00 USD");

  await expectRows(page.getByRole("table", { name: "Holdings in E2E D FHSA" }), [
    [QZHG, "50", "11.00 CAD", "550.00 CAD", "550.00", "No price", "–", "–", "0.00", "0.00"],
  ]);
  await expect(page.getByLabel("Cash in E2E D FHSA")).toHaveText("Cash: 450.00 CAD");

  const managed = page.getByRole("region", { name: "Account E2E D Managed RRSP" });
  await expect(managed.getByRole("heading")).toContainText("Managed, account-level only");
  await expectRows(page.getByRole("table", { name: "Holdings in E2E D Managed RRSP" }), [
    [QZHG, "10", "10.50 CAD", "105.00 CAD", "105.00", "No price", "–", "–", "0.00", "0.00"],
  ]);
});

test("AC-8: combined exposure by security leaves the managed account out and says so", async ({ page }) => {
  await signIn(page);
  await openHoldings(page);
  await page.getByRole("button", { name: "By security" }).click();
  await expect(page.getByRole("button", { name: "By security" })).toHaveAttribute("aria-pressed", "true");
  await expect(page.getByText("Excludes E2E D Managed RRSP: managed account, reported at account level only.")).toBeVisible();
  await expectRows(page.getByRole("table", { name: "Holdings by security" }), [
    [QZHG, "110", "10.4815 CAD", "1,152.97", "No price", "–", "–", "73.07", "6.00", "2"],
    [ZZHU, "5", "20.00 USD", "Unknown", "No price", "–", "–", "0.00", "0.00", "1"],
  ]);
});

test("AC-8: scopes", async ({ page }) => {
  await signIn(page);
  await openHoldings(page);
  const scope = page.getByLabel("Scope");
  await scope.selectOption({ label: "Non-registered" });
  await expect(page.getByText("No accounts in this scope.")).toBeVisible();
  await scope.selectOption({ label: "E2E D FHSA" });
  await expect(page.getByRole("region", { name: /^Account / })).toHaveCount(1);
  await expect(page.getByLabel("Totals")).toContainText("Book value550.00 CAD");
  await scope.selectOption({ label: "Registered" });
  await expect(page.getByRole("region", { name: /^Account / })).toHaveCount(3);
});

test("AC-8: the security page shows every account and traces each row to its source", async ({ page }) => {
  await signIn(page);
  await openHoldings(page);
  await page.getByRole("table", { name: "Holdings in E2E D TFSA" }).getByRole("link", { name: "QZHG (TSX)" }).click();
  await expect(page.getByRole("heading", { level: 1 })).toHaveText("QZHG (TSX) SYNTHETIC HOLDINGS ETF");
  await expect(page.getByText("Not classified yet · trades in CAD")).toBeVisible();

  const combined = page.getByLabel("Combined position");
  await expect(combined).toContainText("Quantity110");
  await expect(combined).toContainText("Average cost (informational)10.4815 CAD");
  await expect(combined).toContainText("Book value1,152.97 CAD");
  await expect(page.getByText("Excludes E2E D Managed RRSP: managed account, reported at account level only.")).toBeVisible();

  await expectRows(page.getByRole("table", { name: "Positions by account" }), [
    ["E2E D FHSARegistered", "50", "11.00 CAD", "550.00 CAD", "550.00", "No price", "–", "0.00", "0.00"],
    ["E2E D Managed RRSPRegistered · managed, account-level only", "10", "10.50 CAD", "105.00 CAD", "105.00",
      "No price", "–", "0.00", "0.00"],
    ["E2E D TFSARegistered", "60", "10.0495 CAD", "602.97 CAD", "602.97", "No price", "–", "73.07", "6.00"],
  ]);

  const ledger = page.getByRole("table", { name: "Ledger rows" });
  await expectRows(ledger, [
    ["2025-01-06", "E2E D TFSA", "Buy", "100", "10", "-1,004.95 CAD", "activities.csv, row 3"],
    ["2025-01-08", "E2E D FHSA", "Buy", "50", "11", "-550.00 CAD", "activities.csv, row 10"],
    ["2025-01-09", "E2E D Managed RRSP", "Buy", "10", "10.5", "-105.00 CAD", "activities.csv, row 12"],
    ["2025-02-10", "E2E D TFSA", "Sell", "40", "12", "475.05 CAD", "activities.csv, row 4"],
    ["2025-03-31", "E2E D TFSA", "Dividend", "", "", "6.00 CAD", "activities.csv, row 5"],
  ]);
  // Rule 5: the source opens the import the row came from.
  await ledger.getByRole("link", { name: "activities.csv, row 4" }).click();
  await expect(page.getByRole("heading", { name: "activities.csv" })).toBeVisible();
  await expect(page.locator(".tag")).toHaveText("Committed");

  // A flagged security explains its condition on its page.
  await page.goBack();
  await page.goBack();
  await expect(page.getByRole("heading", { name: "Holdings" })).toBeVisible();
  await page.getByRole("table", { name: "Holdings in E2E D TFSA" }).getByRole("link", { name: "ZZHU (US)" }).click();
  await expect(page.getByRole("note", { name: "Conditions" })).toContainText(
    "FX rate missing in E2E D TFSA from 2025-04-02",
  );
});

test("FR-1: another user sees none of these holdings", async ({ page, browser }, testInfo) => {
  await signIn(page);
  await openHoldings(page);
  await page.getByRole("table", { name: "Holdings in E2E D TFSA" }).getByRole("link", { name: "QZHG (TSX)" }).click();
  const securityUrl = page.url();

  const other = await browser.newPage();
  await other.goto("/");
  await other.getByLabel("Email").fill(`e2e-b-${testInfo.project.name}@example.test`);
  await other.getByLabel("Password").fill(process.env.E2E_PASSWORD_B ?? "");
  await other.getByRole("button", { name: "Sign in" }).click();
  await expect(other.getByRole("heading", { name: "Accounts" })).toBeVisible();
  await other.getByRole("navigation", { name: "Main" }).getByRole("link", { name: "Holdings" }).click();
  await expect(other.getByText(/No accounts in this scope|Nothing in the ledger yet/)).toBeVisible();
  await other.goto(securityUrl);
  await expect(other.getByRole("alert")).toContainText("security not found");
  await other.close();
});
