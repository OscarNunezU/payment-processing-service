package api

import (
	"errors"
	"net/http"

	"github.com/OscarNunezU/payment-processing-service/backend/internal/core/payment"
	"github.com/OscarNunezU/payment-processing-service/backend/internal/logging"
)

type errorResponse struct {
	Code    string       `json:"code"`
	Message string       `json:"message"`
	Fields  []fieldError `json:"fields,omitempty"`
}

type fieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, errorResponse{Code: code, Message: message})
}

// writeDomainError translates the core's errors into HTTP: ValidationError is
// a 400 and RuleError a 422 with its code. Anything else is a 500 with a
// generic message, so internal details never reach the client.
func writeDomainError(w http.ResponseWriter, r *http.Request, err error) {
	var (
		invalid *payment.ValidationError
		rule    *payment.RuleError
	)
	switch {
	case errors.As(err, &invalid):
		fields := make([]fieldError, len(invalid.Fields))
		for i, f := range invalid.Fields {
			fields[i] = fieldError{Field: f.Field, Message: f.Message}
		}
		writeJSON(w, http.StatusBadRequest, errorResponse{
			Code:    "invalid_request",
			Message: "the request has invalid fields",
			Fields:  fields,
		})
	case errors.As(err, &rule):
		writeError(w, http.StatusUnprocessableEntity, rule.Code, err.Error())
	default:
		logging.FromContext(r.Context()).Error("request failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
	}
}
