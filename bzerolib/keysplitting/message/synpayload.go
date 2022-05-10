package message

import (
	"encoding/base64"
	"fmt"
	"time"

	bzcrt "bastionzero.com/bctl/v1/bzerolib/keysplitting/bzcert"
	"bastionzero.com/bctl/v1/bzerolib/keysplitting/util"
)

// Repetition in Keysplitting messages is requires to maintain flat
// structure which is important for hashing
type SynPayload struct {
	SchemaVersion string `json:"schemaVersion"`
	Type          string `json:"type"`
	Action        string `json:"action"`
	ActionPayload []byte `json:"actionPayload"`
	Timestamp     string `json:"timestamp"`

	// Unique to Syn
	TargetId string       `json:"targetId"`
	Nonce    string       `json:"nonce"`
	BZCert   bzcrt.BZCert `json:"bZCert"`
}

func (s SynPayload) BuildResponsePayload(actionPayload []byte, pubKey string, nonce string, schemaVersion string) (SynAckPayload, error) {
	hashBytes, _ := util.HashPayload(s)
	hash := base64.StdEncoding.EncodeToString(hashBytes)

	return SynAckPayload{
		SchemaVersion:         schemaVersion,
		Type:                  string(SynAck),
		Action:                s.Action,
		ActionResponsePayload: actionPayload,
		TargetPublicKey:       pubKey,
		Nonce:                 nonce,
		HPointer:              hash,
		Timestamp:             fmt.Sprint(time.Now().Unix()),
	}, nil
}
