package domain

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"
)

// SignatureTolerance is how old a signed timestamp a receiver accepts, so a
// webhook captured on the way cannot be replayed later.
const SignatureTolerance = 5 * time.Minute

var (
	// ErrBadSignature means the header is malformed, or its signature does
	// not match the body.
	ErrBadSignature = errors.New("the webhook signature does not match")
	// ErrStaleSignature means the signature is valid but too old.
	ErrStaleSignature = errors.New("the webhook signature is too old")
)

// Sign returns the Webhook-Signature header of body, sent at at (RFC 0001,
// section 9): the unix time, and the hex HMAC-SHA256 of "t.body" with the
// endpoint's secret. The timestamp is signed with the body, so it cannot be
// changed to make an old webhook look new.
func Sign(secret string, body []byte, at time.Time) string {
	t := strconv.FormatInt(at.Unix(), 10)
	return "t=" + t + ",v1=" + mac(secret, t, body)
}

// Verify checks a Webhook-Signature header as a merchant's receiver does: the
// signature must match body, and its timestamp must be within
// SignatureTolerance of now.
func Verify(secret, header string, body []byte, now time.Time) error {
	var t, v1 string
	for part := range strings.SplitSeq(header, ",") {
		key, value, _ := strings.Cut(part, "=")
		switch key {
		case "t":
			t = value
		case "v1":
			v1 = value
		}
	}
	unix, err := strconv.ParseInt(t, 10, 64)
	if err != nil || v1 == "" {
		return ErrBadSignature
	}
	if !hmac.Equal([]byte(v1), []byte(mac(secret, t, body))) {
		return ErrBadSignature
	}
	if age := now.Sub(time.Unix(unix, 0)); age > SignatureTolerance || age < -SignatureTolerance {
		return ErrStaleSignature
	}
	return nil
}

func mac(secret, t string, body []byte) string {
	h := hmac.New(sha256.New, []byte(secret))
	h.Write([]byte(t))
	h.Write([]byte("."))
	h.Write(body)
	return hex.EncodeToString(h.Sum(nil))
}
