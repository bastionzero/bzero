package keysplitting

import (
	"errors"
)

var (
	ErrInvalidSignature = errors.New("keysplitting: invalid signature")
	ErrUnknownHPointer  = errors.New("keysplitting: unknown hpointer")
	ErrMissingLastAck   = errors.New("keysplitting: missing last ack")
)
