package vault

import (
	"bytes"
	"testing"
)

var plaintext = []byte(`{"number":"4111111111111111","expiry_month":12,"expiry_year":2030,"cvv":"123"}`)

func newVault(t *testing.T, keyByte byte) *Vault {
	t.Helper()
	v, err := New(bytes.Repeat([]byte{keyByte}, 32))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return v
}

func TestSealThenOpenReturnsTheOriginal(t *testing.T) {
	v := newVault(t, 0x01)

	sealed, err := v.Seal(plaintext)
	if err != nil {
		t.Fatalf("Seal() error = %v", err)
	}
	if bytes.Contains(sealed, []byte("4111111111111111")) {
		t.Fatal("sealed data contains the card number in clear")
	}

	opened, err := v.Open(sealed)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if !bytes.Equal(opened, plaintext) {
		t.Fatalf("Open() = %s, want %s", opened, plaintext)
	}
}

func TestSealingTwiceGivesDifferentResults(t *testing.T) {
	v := newVault(t, 0x01)

	a, _ := v.Seal(plaintext)
	b, _ := v.Seal(plaintext)
	if bytes.Equal(a, b) {
		t.Fatal("two seals of the same data are equal: the nonce is not random")
	}
}

func TestOpenRejectsTamperedData(t *testing.T) {
	v := newVault(t, 0x01)
	sealed, _ := v.Seal(plaintext)

	sealed[len(sealed)-1] ^= 0xFF

	if _, err := v.Open(sealed); err == nil {
		t.Fatal("Open() accepted altered data")
	}
}

func TestOpenRejectsAnotherKey(t *testing.T) {
	sealed, _ := newVault(t, 0x01).Seal(plaintext)

	if _, err := newVault(t, 0x02).Open(sealed); err == nil {
		t.Fatal("Open() accepted data sealed with another key")
	}
}

func TestNewRejectsAKeyOfTheWrongSize(t *testing.T) {
	if _, err := New([]byte("too-short")); err == nil {
		t.Fatal("New() accepted a key that is not 32 bytes")
	}
}

func TestOpenRejectsDataShorterThanTheNonce(t *testing.T) {
	if _, err := newVault(t, 0x01).Open([]byte("short")); err == nil {
		t.Fatal("Open() accepted data shorter than the nonce")
	}
}
