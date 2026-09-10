// Package apperror gives every service-layer failure a stable machine-readable Code
// and an HTTP status to answer with, so handlers never have to guess how to translate
// an error into a response.
package apperror

import "net/http"

type Code string

const (
	NotRegistered  Code = "NOT_REGISTERED"  // this email+product_code pair has no license_customers row
	QuotaExceeded  Code = "QUOTA_EXCEEDED"  // every ACTIVE seat is taken
	InvalidLicense Code = "INVALID_LICENSE" // signature verification failed
	NotFound       Code = "NOT_FOUND"       // activation/customer/product row not found
	Unauthorized   Code = "UNAUTHORIZED"    // admin session missing/invalid
	Validation     Code = "VALIDATION"      // malformed request
	Internal       Code = "INTERNAL"
)

var httpStatus = map[Code]int{
	NotRegistered:  http.StatusNotFound,
	QuotaExceeded:  http.StatusConflict,
	InvalidLicense: http.StatusUnprocessableEntity,
	NotFound:       http.StatusNotFound,
	Unauthorized:   http.StatusUnauthorized,
	Validation:     http.StatusBadRequest,
	Internal:       http.StatusInternalServerError,
}

type Error struct {
	Code    Code
	Message string
}

func (e *Error) Error() string { return e.Message }

func (e *Error) HTTPStatus() int {
	if status, ok := httpStatus[e.Code]; ok {
		return status
	}
	return http.StatusInternalServerError
}

func New(code Code, message string) *Error {
	return &Error{Code: code, Message: message}
}
