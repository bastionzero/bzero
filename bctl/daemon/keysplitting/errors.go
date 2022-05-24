package keysplitting

import (
	"errors"
)

var (
	ErrInvalidSignature     = errors.New("invalid signature")
	ErrUnknownHPointer      = errors.New("unknown hpointer")
	ErrMissingLastAck       = errors.New("missing last ack")
	ErrFailedToSign         = errors.New("could not sign payload")
	ErrFailedToParseVersion = errors.New("could not parse schema version")
)
