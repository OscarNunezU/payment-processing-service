// Digits after the decimal point in each currency's minor unit (ISO 4217):
// the API sends amounts as integers, so 15000 CLP is $15.000 and 1250 USD is
// $12,50. The screens show the currency code next to the amount, so the
// symbol stays short.
const MINOR_UNIT_DIGITS: Record<string, number> = { CLP: 0, USD: 2 };

// Payments are shown in Chilean time wherever the server runs.
const TIME_ZONE = "America/Santiago";

export function formatAmount(amount: number, currency: string): string {
  const digits = MINOR_UNIT_DIGITS[currency] ?? 2;
  return new Intl.NumberFormat("es-CL", {
    style: "currency",
    currency,
    currencyDisplay: "narrowSymbol",
    minimumFractionDigits: digits,
    maximumFractionDigits: digits,
  }).format(amount / 10 ** digits);
}

function parts(iso: string, options: Intl.DateTimeFormatOptions): Record<string, string> {
  const formatted = new Intl.DateTimeFormat("es-CL", { timeZone: TIME_ZONE, hourCycle: "h23", ...options })
    .formatToParts(new Date(iso));
  return Object.fromEntries(formatted.map((p) => [p.type, p.value]));
}

// "16-10 09:41:05": day, month and time, for lists.
export function formatDateTime(iso: string): string {
  const p = parts(iso, { day: "2-digit", month: "2-digit", hour: "2-digit", minute: "2-digit", second: "2-digit" });
  return `${p.day}-${p.month} ${p.hour}:${p.minute}:${p.second}`;
}

// "09:41:03.120": time with milliseconds, for a payment's history.
export function formatTime(iso: string): string {
  const p = parts(iso, { hour: "2-digit", minute: "2-digit", second: "2-digit", fractionalSecondDigits: 3 });
  return `${p.hour}:${p.minute}:${p.second}.${p.fractionalSecond}`;
}

export function shortId(id: string): string {
  return id.slice(0, 8);
}
