package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/OscarNunezU/payment-processing-service/internal/payments/app"
	"github.com/OscarNunezU/payment-processing-service/internal/payments/domain"
)

// merchants is an in-memory app.Merchants.
type merchants struct {
	byHash map[[sha256.Size]byte]domain.Merchant
	err    error
}

func (s merchants) MerchantByAPIKey(_ context.Context, hash [sha256.Size]byte) (domain.Merchant, error) {
	if s.err != nil {
		return domain.Merchant{}, s.err
	}
	m, ok := s.byHash[hash]
	if !ok {
		return domain.Merchant{}, app.ErrNotFound
	}
	return m, nil
}

func serve(t *testing.T, store app.Merchants, authorization string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	New(store, slog.New(slog.DiscardHandler)).Register(mux)
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/merchant", nil)
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestTheKeyIdentifiesTheMerchant(t *testing.T) {
	secret := domain.NewAPIKey()
	hash, _ := domain.HashAPIKey(secret)
	store := merchants{byHash: map[[sha256.Size]byte]domain.Merchant{hash: {
		ID: "m1", LegalName: "Tienda Uno SpA", RUT: "76543210-3", Status: domain.MerchantActive,
		MaxAmount: map[domain.Currency]int64{domain.CLP: 5_000_000},
	}}}

	rec := serve(t, store, "Bearer "+secret)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d %s, want 200", rec.Code, rec.Body)
	}
	var got merchantResponse
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.ID != "m1" || got.RUT != "76543210-3" || got.Status != domain.MerchantActive || got.MaxAmount[domain.CLP] != 5_000_000 {
		t.Errorf("got %+v", got)
	}
}

func TestRequestsWithoutAValidKeyAre401(t *testing.T) {
	secret := domain.NewAPIKey()
	hash, _ := domain.HashAPIKey(secret)
	store := merchants{byHash: map[[sha256.Size]byte]domain.Merchant{hash: {ID: "m1"}}}
	for _, authorization := range []string{
		"",
		"Bearer " + domain.NewAPIKey(), // unknown
		secret,                         // a real key, but not as a bearer token
		"Token " + secret,
		"Basic dXNlcjpwYXNz",
	} {
		rec := serve(t, store, authorization)
		if rec.Code != http.StatusUnauthorized || rec.Header().Get("WWW-Authenticate") == "" {
			t.Errorf("%q: got %d with WWW-Authenticate %q, want 401 with the header", authorization, rec.Code, rec.Header().Get("WWW-Authenticate"))
		}
	}
}

func TestTheBearerSchemeIsCaseInsensitive(t *testing.T) {
	secret := domain.NewAPIKey()
	hash, _ := domain.HashAPIKey(secret)
	store := merchants{byHash: map[[sha256.Size]byte]domain.Merchant{hash: {ID: "m1"}}}
	for _, scheme := range []string{"Bearer", "bearer", "BEARER"} {
		if rec := serve(t, store, scheme+" "+secret); rec.Code != http.StatusOK {
			t.Errorf("%s: got %d, want 200", scheme, rec.Code)
		}
	}
}

func TestAStoreFailureIs503NotA401(t *testing.T) {
	rec := serve(t, merchants{err: errors.New("connection refused")}, "Bearer "+domain.NewAPIKey())
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") == "" {
		t.Errorf("got %d with Retry-After %q, want 503 with the header", rec.Code, rec.Header().Get("Retry-After"))
	}
}
