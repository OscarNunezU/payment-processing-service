import { CARDS, expect, pay, shortId, test } from "./fixtures";

test("a payment's webhook arrives with a valid signature", async ({ page }) => {
  const id = await pay(page, CARDS.approves);
  await page.goto("/webhooks");
  const row = page.locator("tbody tr", { hasText: shortId(id) });
  await expect(row).toContainText("payment.approved", { timeout: 15_000 });
  await expect(row).toContainText("válida");
});

// Feature: Webhooks. Scenario: The merchant's endpoint is down.
test("with the endpoint down, the webhook is retried until it is delivered", async ({ page }) => {
  await page.goto("/webhooks");
  await page.getByRole("button", { name: "Simular el endpoint caído" }).click();
  await expect(page.getByRole("button", { name: "Volver a responder 200" })).toBeVisible();
  try {
    const id = await pay(page, CARDS.declines);
    await page.goto("/webhooks");
    // Refused with a 500: nothing is listed while the endpoint is down.
    await page.waitForTimeout(3000);
    await expect(page.locator("tbody tr", { hasText: shortId(id) })).toHaveCount(0);

    await page.getByRole("button", { name: "Volver a responder 200" }).click();
    // The next attempt comes 5 to 10 s after the first.
    await expect(page.locator("tbody tr", { hasText: shortId(id) })).toContainText("válida", { timeout: 30_000 });
  } finally {
    await page.goto("/webhooks");
    const restore = page.getByRole("button", { name: "Volver a responder 200" });
    if (await restore.isVisible()) await restore.click();
  }
});
