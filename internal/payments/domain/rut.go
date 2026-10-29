// Package domain holds the rules of the payment service: merchants, payments
// and their states, in plain Go with no I/O (ADR 0004).
package domain

import (
	"errors"
	"strings"
)

// ErrInvalidRUT means a RUT is malformed or its check digit does not match.
var ErrInvalidRUT = errors.New("the RUT is not valid")

// RUT is Chile's tax id, in its canonical form: the number without dots, a
// hyphen and the check digit, such as "76543210-3".
type RUT string

// ParseRUT accepts a RUT with or without dots and hyphen, and with the check
// digit K in either case. It checks the digit with modulo 11.
func ParseRUT(s string) (RUT, error) {
	s = strings.ToUpper(strings.NewReplacer(".", "", "-", "", " ", "").Replace(s))
	if len(s) < 2 || len(s) > 9 {
		return "", ErrInvalidRUT
	}
	body, digit := s[:len(s)-1], s[len(s)-1]
	if body[0] == '0' || strings.Trim(body, "0123456789") != "" {
		return "", ErrInvalidRUT
	}
	if checkDigit(body) != digit {
		return "", ErrInvalidRUT
	}
	return RUT(body + "-" + string(digit)), nil
}

// checkDigit computes the check digit of a RUT's number: each digit, from the
// right, is weighted 2, 3, 4, 5, 6, 7, 2, 3…, and 11 minus the sum modulo 11
// gives the digit, with 11 written 0 and 10 written K.
func checkDigit(body string) byte {
	sum, weight := 0, 2
	for i := len(body) - 1; i >= 0; i-- {
		sum += int(body[i]-'0') * weight
		weight++
		if weight > 7 {
			weight = 2
		}
	}
	switch d := 11 - sum%11; d {
	case 11:
		return '0'
	case 10:
		return 'K'
	default:
		return byte('0' + d)
	}
}
