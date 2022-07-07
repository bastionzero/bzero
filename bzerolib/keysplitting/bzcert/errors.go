package bzcert

import (
	"fmt"
)

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
