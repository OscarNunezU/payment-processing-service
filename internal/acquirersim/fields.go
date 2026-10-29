package acquirersim

import (
	"crypto/subtle"
	"embed"
	"html/template"
	"net/http"
	"slices"
)

// The card fields: a page that a merchant's checkout embeds in an iframe, so
// the cardholder types the card on the acquirer's origin and the merchant
// only ever gets a token (RFC 0001, section 12). Its script and styles are
// files of their own, so its CSP allows no inline code.

//go:embed fields
var fieldsFiles embed.FS

var fieldsPage = template.Must(template.ParseFS(fieldsFiles, "fields/fields.html"))

// fieldsTestCard is a test card the fields offer, so a tester never types one
// into the merchant's page.
type fieldsTestCard struct {
	Number, Last4, Label string
}

// fieldsTestCards are the test cards of RFC 0001, section 10, in its order.
var fieldsTestCards = []fieldsTestCard{
	{"4111111111111111", "1111", "se aprueba"},
	{"4000000000000002", "0002", "rechazada: card_declined"},
	{"4000000000009995", "9995", "rechazada: insufficient_funds"},
	{"4000000000000127", "0127", "503 la primera vez, y aprobada"},
	{"4000000000000135", "0135", "aprobada, pero responde tarde"},
	{"4000000000000150", "0150", "nunca responde"},
	{"4000000000000119", "0119", "503 siempre"},
	{"4000000000000143", "0143", "el adquirente rechaza la request"},
}

// fields serves the page to the merchant pages allowed to embed it. The
// merchant passes its publishable key and its origin. The page answers only
// to that origin, and frame-ancestors keeps any other page from embedding it.
func (s *Simulator) fields(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if subtle.ConstantTimeCompare([]byte(q.Get("key")), []byte(s.cfg.PublishableKey)) != 1 {
		http.Error(w, "Unknown publishable key.", http.StatusUnauthorized)
		return
	}
	origin := q.Get("origin")
	if !slices.Contains(s.cfg.AllowedOrigins, origin) {
		http.Error(w, "This page may not embed the card fields.", http.StatusForbidden)
		return
	}
	theme := q.Get("theme")
	if theme != "light" && theme != "dark" {
		theme = ""
	}

	h := w.Header()
	// origin comes from the allowed list, never from the request as is.
	h.Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; "+
		"form-action 'none'; base-uri 'none'; frame-ancestors "+origin)
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Cache-Control", "no-store")
	if err := fieldsPage.Execute(w, map[string]any{
		"Origin": origin, "Key": s.cfg.PublishableKey, "Theme": theme, "TestCards": fieldsTestCards,
	}); err != nil {
		s.log.ErrorContext(r.Context(), "could not render the card fields", "error", err)
	}
}

// fieldsAsset serves one of the page's files.
func fieldsAsset(name, contentType string) http.Handler {
	body, err := fieldsFiles.ReadFile("fields/" + name)
	if err != nil {
		panic(err) // embedded at build time: it is there
	}
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		_, _ = w.Write(body)
	})
}
