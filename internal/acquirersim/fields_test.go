package acquirersim

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// page is a GET answer of the public port, once its body is read.
type page struct {
	status int
	header http.Header
	body   string
}

// get fetches path from the public port.
func (h *harness) get(t *testing.T, path string) page {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, h.public.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return page{resp.StatusCode, resp.Header, string(body)}
}

func fieldsPath(key, origin string) string {
	return "/v1/fields?" + url.Values{"key": {key}, "origin": {origin}}.Encode()
}

func TestTheCardFieldsAnswerOnlyToTheirMerchant(t *testing.T) {
	h := newHarness(t)
	p := h.get(t, fieldsPath(publishableKey, checkoutOrigin))
	if p.status != http.StatusOK || !strings.HasPrefix(p.header.Get("Content-Type"), "text/html") {
		t.Fatalf("got %d %s, want the page", p.status, p.header.Get("Content-Type"))
	}
	csp := p.header.Get("Content-Security-Policy")
	for _, directive := range []string{"frame-ancestors " + checkoutOrigin, "script-src 'self'", "connect-src 'self'", "default-src 'none'"} {
		if !strings.Contains(csp, directive) {
			t.Errorf("the CSP %q lacks %q", csp, directive)
		}
	}
	if !strings.Contains(p.body, `data-origin="`+checkoutOrigin+`"`) || strings.Contains(p.body, "<script>") {
		t.Errorf("the page does not name its merchant's origin, or has inline code:\n%s", p.body)
	}
	for _, card := range fieldsTestCards {
		if _, ok := testCards[card.Number]; !ok || !strings.HasSuffix(card.Number, card.Last4) {
			t.Errorf("the fields offer %s, which is not a test card or is not ••••%s", card.Number, card.Last4)
		}
	}
}

func TestOnlyAllowedPagesMayEmbedTheCardFields(t *testing.T) {
	h := newHarness(t)
	if p := h.get(t, fieldsPath(publishableKey, "https://evil.example")); p.status != http.StatusForbidden {
		t.Errorf("another origin: got %d, want 403", p.status)
	}
	if p := h.get(t, fieldsPath("pk_other", checkoutOrigin)); p.status != http.StatusUnauthorized {
		t.Errorf("another key: got %d, want 401", p.status)
	}
	// The theme is a choice between two words, not text for the page.
	if p := h.get(t, fieldsPath(publishableKey, checkoutOrigin)+"&theme=%22%3E%3Cscript%3E"); strings.Contains(p.body, "<script>") {
		t.Error("a theme put markup in the page")
	}
}

func TestTheCardFieldsServeTheirScriptAndStyles(t *testing.T) {
	h := newHarness(t)
	for path, contentType := range map[string]string{"/v1/fields.js": "text/javascript", "/v1/fields.css": "text/css"} {
		p := h.get(t, path)
		if p.status != http.StatusOK || !strings.HasPrefix(p.header.Get("Content-Type"), contentType) || p.body == "" {
			t.Errorf("%s: got %d %s with %d bytes", path, p.status, p.header.Get("Content-Type"), len(p.body))
		}
	}
}
