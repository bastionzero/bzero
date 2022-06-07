package bzcert

import (
	"encoding/base64"

	"bastionzero.com/bctl/v1/bzerolib/keysplitting/util"
)

type BZCert struct {
	InitialIdToken  string `json:"initialIdToken"`
	CurrentIdToken  string `json:"currentIdToken"`
	ClientPublicKey string `json:"clientPublicKey"`
	Rand            string `json:"rand"`
	SignatureOnRand string `json:"signatureOnRand"`
}

func (b *BZCert) Hash() (string, bool) {
	if hashBytes, ok := util.HashPayload((*b)); ok {
		return base64.StdEncoding.EncodeToString(hashBytes), ok
	} else {
		return "", ok
	}
}
