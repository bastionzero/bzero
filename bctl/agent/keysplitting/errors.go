package keysplitting

import (
	"errors"
)

var (
	ErrInvalidSignature     = errors.New("invalid signature")
	ErrFailedToParseVersion = errors.New("could not parse schema version")
	ErrTargetIdMismatch     = errors.New("target id does not match agent's public key")
	ErrBZCertMismatch       = errors.New("BZCert hash does not match the current keysplitting session's BZCert")
	ErrBZCertExpired        = errors.New("BZCert has expired")
	ErrUnexpectedHPointer   = errors.New("unexpected HPointer")
)
