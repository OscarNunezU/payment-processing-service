package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/OscarNunezU/payment-processing-service/internal/payments/app"
	"github.com/OscarNunezU/payment-processing-service/internal/payments/app/apptest"
	"github.com/OscarNunezU/payment-processing-service/internal/payments/domain"
)

var tiendaUno = domain.Merchant{
	ID: "00000000-0000-4000-8000-000000000001", LegalName: "Tienda Uno SpA", RUT: "76543210-3",
	Status: domain.MerchantActive, MaxAmount: map[domain.Currency]int64{domain.CLP: 5_000_000},
}

// testAPI is the API on an in-memory store, with one merchant and its key.
type testAPI struct {
	store *apptest.Store
	mux   *http.ServeMux
	key   string
}

func newTestAPI(t *testing.T) *testAPI {
	t.Helper()
	store := apptest.NewStore()
	svc := app.NewService(store, store, func() string { return uuid.NewV7().String() }, time.Now)
	mux := http.NewServeMux()
	New(svc, slog.New(slog.DiscardHandler)).Register(mux)
	return &testAPI{store: store, mux: mux, key: store.AddMerchant(tiendaUno)}
}

// do sends a request with the given Authorization header, and the headers as
// name and value pairs.
func (a *testAPI) do(t *testing.T, method, path, authorization, body string, headers ...string) *httptest.ResponseRecorder {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req := httptest.NewRequestWithContext(t.Context(), method, path, r)
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	a.mux.ServeHTTP(rec, req)
	return rec
}

func decodeInto[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.NewDecoder(rec.Body).Decode(&v); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return v
}

func TestTheKeyIdentifiesTheMerchant(t *testing.T) {
	api := newTestAPI(t)
	rec := api.do(t, http.MethodGet, "/v1/merchant", "Bearer "+api.key, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d %s, want 200", rec.Code, rec.Body)
	}
	got := decodeInto[merchantResponse](t, rec)
	if got.ID != tiendaUno.ID || got.RUT != "76543210-3" || got.Status != domain.MerchantActive || got.MaxAmount[domain.CLP] != 5_000_000 {
		t.Errorf("got %+v", got)
	}
}

func TestRequestsWithoutAValidKeyAre401(t *testing.T) {
	api := newTestAPI(t)
	for _, authorization := range []string{
		"",
		"Bearer " + domain.NewAPIKey(), // unknown
		api.key,                        // a real key, but not as a bearer token
		"Token " + api.key,
		"Basic dXNlcjpwYXNz",
	} {
		for _, route := range []string{"GET /v1/merchant", "POST /v1/payments", "GET /v1/payments", "GET /v1/payments/x"} {
			method, path, _ := strings.Cut(route, " ")
			rec := api.do(t, method, path, authorization, "")
			if rec.Code != http.StatusUnauthorized || rec.Header().Get("WWW-Authenticate") == "" {
				t.Errorf("%s with %q: got %d, want 401 with WWW-Authenticate", route, authorization, rec.Code)
			}
		}
	}
}

func TestTheBearerSchemeFollowsHTTP(t *testing.T) {
	api := newTestAPI(t)
	// The scheme is case-insensitive, and one or more spaces separate it from
	// the credentials.
	for _, authorization := range []string{"Bearer " + api.key, "bearer " + api.key, "BEARER " + api.key, "Bearer   " + api.key} {
		if rec := api.do(t, http.MethodGet, "/v1/merchant", authorization, ""); rec.Code != http.StatusOK {
			t.Errorf("%q: got %d, want 200", authorization, rec.Code)
		}
	}
}

func TestAStoreFailureIs503NotA401(t *testing.T) {
	api := newTestAPI(t)
	api.store.Err = errors.New("connection refused")
	rec := api.do(t, http.MethodGet, "/v1/merchant", "Bearer "+api.key, "")
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") == "" {
		t.Errorf("got %d with Retry-After %q, want 503 with the header", rec.Code, rec.Header().Get("Retry-After"))
	}
}
