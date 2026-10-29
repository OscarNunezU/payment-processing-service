import { CARDS, cardFields, expect, pay, test } from "./fixtures";

test("an approval is shown with its authorization code", async ({ page }) => {
  await pay(page, CARDS.approves);
  await expect(page.getByText("APPROVED").first()).toBeVisible();
  await expect(page.getByText("Autorización")).toBeVisible();
  await expect(page.getByText("Intento 1")).toBeVisible();
});

test("a decline is shown with its code", async ({ page }) => {
  await pay(page, CARDS.declines);
  await expect(page.getByText("DECLINED").first()).toBeVisible();
  await expect(page.getByText("card_declined")).toBeVisible();
});

// The answer is 202, and the page follows the payment while the recovery
// finishes it.
test("a payment in doubt is followed until the recovery approves it", async ({ page }) => {
  await pay(page, CARDS.unavailableOnce);
  await expect(page.getByText("Actualizando mientras se procesa")).toBeVisible();
  await expect(page.getByText("APPROVED").first()).toBeVisible({ timeout: 20_000 });
  await expect(page.getByText("Intento 2")).toBeVisible();
  await expect(page.getByText("Actualizando mientras se procesa")).toBeHidden();
});

// NFR06: the card goes from the acquirer's fields to the acquirer, and the
// merchant's page and server only ever see a token.
test("the card number never reaches the merchant's page or server", async ({ page, baseURL }) => {
  const number = CARDS.approves;
  const leaks: string[] = [];
  page.on("request", (r) => {
    const fromMerchant = r.frame() === page.mainFrame() || r.url().startsWith(baseURL!);
    if (fromMerchant && (r.postData() ?? "").replace(/\D/g, "").includes(number)) leaks.push(`${r.method()} ${r.url()}`);
  });

  await page.goto("/checkout");
  // As a cardholder would: typed, not chosen.
  const fields = cardFields(page);
  await fields.locator("#number").pressSequentially(number);
  await fields.locator("#expiry").pressSequentially("1235");
  await fields.locator("#cvc").pressSequentially("123");
  expect(await page.content()).not.toContain(number);
  await page.getByRole("button", { name: "Pagar" }).click();
  await page.waitForURL(/\/payments\/[0-9a-f-]{36}$/);

  await expect(page.getByText("APPROVED").first()).toBeVisible();
  await expect(page.getByText("•••• 1111")).toBeVisible();
  expect(leaks).toEqual([]);
});

test("the list filters by status", async ({ page }) => {
  await pay(page, CARDS.declines);
  await page.goto("/");
  await page.getByLabel("Estado").selectOption("DECLINED");
  await page.getByRole("button", { name: "Filtrar" }).click();
  await expect(page).toHaveURL(/status=DECLINED/);
  // The filtered table replaces the other one once the API answers.
  const statuses = page.locator("tbody tr td:last-child");
  await expect
    .poll(async () => {
      const texts = await statuses.allInnerTexts();
      return texts.length > 0 && texts.every((t) => t.includes("DECLINED"));
    })
    .toBe(true);
});

test("the menu marks the section of the current page", async ({ page }) => {
  const id = await pay(page, CARDS.approves);
  const menu = page.getByRole("navigation", { name: "Secciones" });
  // A payment's page belongs to the payments.
  await expect(menu.getByRole("link", { name: "Pagos" })).toHaveAttribute("aria-current", "page");
  for (const [path, section] of [["/webhooks", "Webhooks"], ["/acquirer", "Adquirente"], ["/", "Pagos"]]) {
    await page.goto(path);
    await expect(menu.getByRole("link", { name: section })).toHaveAttribute("aria-current", "page");
    await expect(menu.locator('[aria-current="page"]')).toHaveCount(1);
  }
  expect(id).toBeTruthy();
});
