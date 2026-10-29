import { cardFields, expect, test } from "./fixtures";

// The iframe of a closed port fails to load, which the browser logs.
test.use({ allowConsole: [/ERR_CONNECTION_REFUSED|Failed to load resource/] });

test("with the acquirer down, the card cannot even be entered, and nothing is paid", async ({ page }) => {
  await page.goto("/acquirer");
  await page.getByLabel(/Caído/).check();
  await page.getByRole("button", { name: "Guardar" }).click();
  await expect(page.getByLabel(/Caído/)).toBeChecked();
  try {
    await page.goto("/checkout");
    await expect(page.getByText("No se pudo cargar el formulario de la tarjeta")).toBeVisible({ timeout: 10_000 });
    await expect(page.getByRole("button", { name: "Pagar" })).toBeDisabled();
  } finally {
    await page.goto("/acquirer");
    await page.getByLabel(/Caído/).uncheck();
    await page.getByRole("button", { name: "Guardar" }).click();
    await expect(page.getByLabel(/Caído/)).not.toBeChecked();
  }
  await page.goto("/checkout");
  await expect(cardFields(page).locator("#number")).toBeVisible();
});
