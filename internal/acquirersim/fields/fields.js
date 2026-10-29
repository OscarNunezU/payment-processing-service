// The acquirer's card fields (RFC 0001, section 12). A merchant's checkout
// embeds this page in an iframe and talks to it with postMessage: it asks for
// a token, and gets only the token back. The card number never leaves the
// acquirer's origin.
(() => {
  const { origin, key } = document.body.dataset;
  const field = (id) => document.getElementById(id);
  const number = field("number");
  const expiry = field("expiry");
  const cvc = field("cvc");
  const error = field("error");

  const MESSAGES = {
    invalid_number: "El número de tarjeta no es válido.",
    invalid_expiry: "El vencimiento no es válido.",
    expired_card: "La tarjeta está vencida.",
    invalid_cvc: "El CVC debe tener 3 o 4 dígitos.",
  };

  // Only the merchant's page that embeds this one hears from it.
  const post = (message) => window.parent.postMessage(message, origin);
  const digits = (s) => s.replace(/\D/g, "");

  function card() {
    const [month, year] = expiry.value.split("/");
    return {
      number: digits(number.value),
      exp_month: Number(month),
      exp_year: 2000 + Number(year),
      cvc: digits(cvc.value),
    };
  }

  function complete() {
    const c = card();
    return c.number.length >= 12 && c.exp_month >= 1 && c.exp_month <= 12 && c.exp_year > 2000 && c.cvc.length >= 3;
  }

  function changed() {
    error.textContent = "";
    post({ type: "change", complete: complete() });
  }

  function setNumber(value) {
    number.value = digits(value).slice(0, 19).replace(/(\d{4})(?=\d)/g, "$1 ");
  }

  number.addEventListener("input", () => {
    setNumber(number.value);
    changed();
  });
  expiry.addEventListener("input", () => {
    const d = digits(expiry.value).slice(0, 4);
    expiry.value = d.length > 2 ? `${d.slice(0, 2)}/${d.slice(2)}` : d;
    changed();
  });
  cvc.addEventListener("input", () => {
    cvc.value = digits(cvc.value).slice(0, 4);
    changed();
  });
  field("test-card").addEventListener("change", (e) => {
    if (!e.target.value) return;
    setNumber(e.target.value);
    expiry.value = "12/35";
    cvc.value = "123";
    changed();
  });

  // The same card gets the same token while the page lives, so a checkout
  // that sends its payment again, with the same idempotency key, sends the
  // same request.
  let last = null;

  async function tokenize() {
    const c = card();
    const id = JSON.stringify(c);
    if (last && last.id === id) return last.answer;
    try {
      const res = await fetch("/v1/tokens", {
        method: "POST",
        headers: { Authorization: `Bearer ${key}`, "Content-Type": "application/json" },
        body: JSON.stringify(c),
      });
      const body = await res.json();
      if (!res.ok) {
        return { type: "error", message: MESSAGES[body.error && body.error.code] || "El adquirente rechazó la tarjeta." };
      }
      last = { id, answer: { type: "token", token: body.token, brand: body.brand, last4: body.last4 } };
      return last.answer;
    } catch {
      return { type: "error", message: "El adquirente no responde." };
    }
  }

  window.addEventListener("message", async (e) => {
    if (e.origin !== origin || e.source !== window.parent) return;
    if (e.data && e.data.type === "tokenize") {
      const answer = await tokenize();
      if (answer.type === "error") error.textContent = answer.message;
      post(answer);
    }
  });

  const height = () => document.documentElement.scrollHeight;
  post({ type: "ready", height: height() });
  new ResizeObserver(() => post({ type: "resize", height: height() })).observe(document.body);
})();
