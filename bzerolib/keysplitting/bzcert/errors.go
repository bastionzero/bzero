package bzcert

import (
	"fmt"
)

// These errors follow to go convention of providing a Unwrap method which
// allows the inner error to be unwrapped further up the call stack and checked
// via errors.Is or errors.As. See https://go.dev/blog/go1.13-errors

type InitialIdTokenError struct {
	message    string
	innerError error
}

func NewInitialIdTokenError(innerError error) *InitialIdTokenError {
	return &InitialIdTokenError{
		message:    fmt.Sprintf("error verifying initial id token: %s", innerError),
		innerError: innerError,
	}
}

func (e *InitialIdTokenError) Error() string {
	return e.message
}

func (e *InitialIdTokenError) Unwrap() error { return e.innerError }

type CurrentIdTokenError struct {
	message    string
	innerError error
}

func NewCurrentIdTokenError(innerError error) *CurrentIdTokenError {
	return &CurrentIdTokenError{
		message:    fmt.Sprintf("error verifying current id token: %s", innerError),
		innerError: innerError,
	}
}

func (e *CurrentIdTokenError) Error() string {
	return e.message
}

func (e *CurrentIdTokenError) Unwrap() error { return e.innerError }
