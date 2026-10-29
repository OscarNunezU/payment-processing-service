import { expect, type Page, test as base } from "@playwright/test";

// The test cards of RFC 0001, section 10.
export const CARDS = {
  approves: "4111111111111111",
  declines: "4000000000000002",
  unavailableOnce: "4000000000000127",
} as const;

type Fixtures = {
  // allowConsole lists the console errors a test expects, such as the
  // iframe of an acquirer that is down.
  allowConsole: RegExp[];
};

// test fails any test whose pages log a warning or an error it did not
// expect: React warns in the console, and so does a failed request.
export const test = base.extend<Fixtures>({
  allowConsole: [[], { option: true }],
  page: async ({ page, allowConsole }, use) => {
    const issues: string[] = [];
    page.on("console", (m) => {
      if ((m.type() === "warning" || m.type() === "error") && !allowConsole.some((r) => r.test(m.text()))) {
        issues.push(`${m.type()}: ${m.text()}`);
      }
    });
    page.on("pageerror", (e) => issues.push(`page error: ${e.message}`));
    await use(page);
    expect(issues, "console warnings and errors").toEqual([]);
  },
});

export { expect };

export const cardFields = (page: Page) => page.frameLocator('iframe[title="Datos de la tarjeta"]');

// pay goes through the checkout with a test card, chosen inside the
// acquirer's fields, and returns the payment's id once its page opens.
export async function pay(page: Page, card: string): Promise<string> {
  await page.goto("/checkout");
  await cardFields(page).locator("#test-card").selectOption(card);
  await page.getByRole("button", { name: "Pagar" }).click();
  await page.waitForURL(/\/payments\/[0-9a-f-]{36}$/);
  return page.url().split("/").pop()!;
}

// shortId is how the panel shows an id: its random end.
export const shortId = (id: string) => id.slice(-8);
