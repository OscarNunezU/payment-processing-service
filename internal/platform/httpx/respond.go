package httpx

import (
	"encoding/json"
	"net/http"
)

// ErrorBody is the error format of every endpoint (RFC 0001, section 11).
type ErrorBody struct {
	Error ErrorDetail `json:"error"`
}

// ErrorDetail carries a stable code for programs and a message for people.
// Fields lists every malformed field of a request, so a client can fix them
// all in one round trip.
type ErrorDetail struct {
	Code    string       `json:"code"`
	Message string       `json:"message"`
	Fields  []FieldError `json:"fields,omitempty"`
}

// FieldError is one malformed field, named as the client sent it.
type FieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// WriteJSON writes v as the JSON body of a response with status.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	// The status is already sent: if the client went away, there is no one
	// left to tell.
	_ = json.NewEncoder(w).Encode(v)
}

// WriteError writes an error response with status.
func WriteError(w http.ResponseWriter, status int, code, message string) {
	WriteJSON(w, status, ErrorBody{Error: ErrorDetail{Code: code, Message: message}})
}
