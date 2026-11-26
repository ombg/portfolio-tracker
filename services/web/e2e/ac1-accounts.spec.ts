import { expect, test, type Page } from "@playwright/test";

// PRD AC-1: an authenticated user can create separate Wealthsimple and Questrade accounts, including registered and
// non-registered accounts. Also FR-2 (edit, close, archive) and FR-1 (another user sees none of it).
// Users are synthetic and created by scripts/e2e.sh with ptctl, one pair per browser project (D-20).

let userA = { email: "", password: "" };
let userB = { email: "", password: "" };

test.describe.configure({ mode: "serial" });

async function signIn(page: Page, email: string, password: string) {
  await page.goto("/");
  await page.getByLabel("Email").fill(email);
  await page.getByLabel("Password").fill(password);
  await page.getByRole("button", { name: "Sign in" }).click();
}

async function addAccount(
  page: Page,
  a: { broker: string; name: string; type: string; currency?: string; management?: string; number?: string },
) {
  await page.getByRole("button", { name: "Add account" }).click();
  const form = page.getByRole("form", { name: "New account" });
  await form.getByLabel("Broker").selectOption({ label: a.broker });
  await form.getByLabel("Name").fill(a.name);
  await form.getByLabel("Account type").selectOption({ label: a.type });
  if (a.currency) await form.getByLabel("Base currency").selectOption(a.currency);
  if (a.management) await form.getByLabel("Management").selectOption({ label: a.management });
  if (a.number) await form.getByLabel("Account number").fill(a.number);
  await form.getByRole("button", { name: "Create account" }).click();
  await expect(form).toBeHidden();
}

function row(page: Page, name: string) {
  return page.getByRole("table", { name: "Accounts" }).getByRole("row").filter({ hasText: name });
}

const escape = (s: string) => s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");

// Compares every cell of the row exactly (name may carry a tag), so one column cannot mask another.
async function expectRow(
  page: Page,
  name: string,
  cells: { broker: string; type: string; tax: "Registered" | "Non-registered"; currency: string; status: string; tag?: string },
) {
  await expect(row(page, name).getByRole("cell")).toHaveText([
    new RegExp(`^${escape(name)}${cells.tag ? escape(cells.tag) : ""}$`),
    cells.broker,
    cells.type,
    cells.tax,
    cells.currency,
    cells.status,
    new RegExp(`^Edit ${escape(name)}$`),
  ]);
}

test.beforeAll(({}, testInfo) => {
  const browser = testInfo.project.name;
  userA = { email: `e2e-a-${browser}@example.test`, password: process.env.E2E_PASSWORD_A ?? "" };
  userB = { email: `e2e-b-${browser}@example.test`, password: process.env.E2E_PASSWORD_B ?? "" };
  expect(userA.password, "run through make e2e").not.toBe("");
});

test("wrong password is refused with a message", async ({ page }) => {
  await signIn(page, userA.email, "definitely not the password");
  await expect(page.getByRole("alert")).toHaveText("Email or password is incorrect.");
  await expect(page.getByRole("heading", { name: "Sign in" })).toBeVisible();
});

test("AC-1: create Questrade and Wealthsimple accounts, registered and non-registered", async ({ page }) => {
  await signIn(page, userA.email, userA.password);
  await expect(page.getByRole("heading", { name: "Accounts" })).toBeVisible();
  await expect(page.getByText("No accounts yet")).toBeVisible();

  await addAccount(page, { broker: "Questrade", name: "E2E QT TFSA", type: "TFSA", number: "90000001" });
  await addAccount(page, { broker: "Questrade", name: "E2E QT Margin", type: "Non-registered", currency: "USD" });
  await addAccount(page, { broker: "Wealthsimple", name: "E2E WS FHSA", type: "FHSA" });
  await addAccount(page, {
    broker: "Wealthsimple",
    name: "E2E WS Managed RRSP",
    type: "RRSP",
    management: "Robo / managed portfolio",
  });

  const table = page.getByRole("table", { name: "Accounts" });
  await expect(table.getByRole("row")).toHaveCount(5); // header + 4
  await expectRow(page, "E2E QT TFSA", { broker: "Questrade", type: "TFSA", tax: "Registered", currency: "CAD", status: "open" });
  await expectRow(page, "E2E QT Margin", {
    broker: "Questrade",
    type: "Non-registered",
    tax: "Non-registered",
    currency: "USD",
    status: "open",
  });
  await expectRow(page, "E2E WS FHSA", { broker: "Wealthsimple", type: "FHSA", tax: "Registered", currency: "CAD", status: "open" });
  await expectRow(page, "E2E WS Managed RRSP", {
    broker: "Wealthsimple",
    type: "RRSP",
    tax: "Registered",
    currency: "CAD",
    status: "open",
    tag: "Managed, account-level only",
  });

  // Survives a reload: the accounts come from the server, not page state.
  await page.reload();
  await expect(table.getByRole("row")).toHaveCount(5);
});

test("duplicate account number at the same broker is refused with the reason", async ({ page }) => {
  await signIn(page, userA.email, userA.password);
  await page.getByRole("button", { name: "Add account" }).click();
  const form = page.getByRole("form", { name: "New account" });
  await form.getByLabel("Broker").selectOption({ label: "Questrade" });
  await form.getByLabel("Name").fill("E2E duplicate");
  await form.getByLabel("Account number").fill("90000001");
  await form.getByRole("button", { name: "Create account" }).click();
  await expect(form.getByRole("alert")).toContainText("already has this account number");
  await form.getByRole("button", { name: "Cancel" }).click();
});

test("FR-2: edit, close and archive an account", async ({ page }) => {
  await signIn(page, userA.email, userA.password);

  await page.getByRole("button", { name: "Edit E2E QT Margin" }).click();
  let form = page.getByRole("form", { name: "Edit E2E QT Margin" });
  await expect(form.getByLabel("Broker")).toBeDisabled();
  await form.getByLabel("Name").fill("E2E QT Margin (old)");
  await form.getByLabel("Status").selectOption("closed");
  await form.getByLabel("Closed on").fill("2025-12-31");
  await form.getByRole("button", { name: "Save changes" }).click();
  await expect(form).toBeHidden();
  await expectRow(page, "E2E QT Margin (old)", {
    broker: "Questrade",
    type: "Non-registered",
    tax: "Non-registered",
    currency: "USD",
    status: "closed",
  });

  await page.getByRole("button", { name: "Edit E2E WS FHSA" }).click();
  form = page.getByRole("form", { name: "Edit E2E WS FHSA" });
  await form.getByLabel("Status").selectOption("archived");
  await form.getByRole("button", { name: "Save changes" }).click();
  await expect(form).toBeHidden();
  await expect(row(page, "E2E WS FHSA")).toHaveCount(0);

  await page.getByLabel("Show archived").check();
  await expectRow(page, "E2E WS FHSA", { broker: "Wealthsimple", type: "FHSA", tax: "Registered", currency: "CAD", status: "archived" });
});

test("FR-1: another user sees none of these accounts", async ({ page }) => {
  await signIn(page, userB.email, userB.password);
  await expect(page.getByRole("heading", { name: "Accounts" })).toBeVisible();
  await expect(page.getByText("No accounts yet")).toBeVisible();
  await page.getByLabel("Show archived").check();
  await expect(page.getByText("No accounts yet")).toBeVisible();
});

test("a session cookie the browser drops is reported, not shown as a silent return to sign in", async ({ page }) => {
  // Simulate a browser that refuses the cookie (Safari with a Secure cookie over plain HTTP, D-34): the login
  // succeeds but no cookie arrives. Answered here, not by the API, so no cookie can reach the browser.
  await page.route("**/api/auth/login", (route) => route.fulfill({ status: 204 }));
  await signIn(page, userA.email, userA.password);
  await expect(page.getByRole("alert")).toContainText("did not keep the session cookie");
  await expect(page.getByRole("heading", { name: "Sign in" })).toBeVisible();
});

test("sign out returns to the login page and the session is gone", async ({ page }) => {
  await signIn(page, userA.email, userA.password);
  await page.getByRole("button", { name: "Sign out" }).click();
  await expect(page.getByRole("heading", { name: "Sign in" })).toBeVisible();
  await page.reload();
  await expect(page.getByRole("heading", { name: "Sign in" })).toBeVisible();
  const res = await page.request.get("/api/accounts");
  expect(res.status()).toBe(401);
});
