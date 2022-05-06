package message

import (
	"encoding/base64"

	"bastionzero.com/bctl/v1/bzerolib/keysplitting/util"
)

type DataPayload struct {
	SchemaVersion string `json:"schemaVersion"`
	Type          string `json:"type"`
	Action        string `json:"action"`
	Timestamp     string `json:"timestamp"`

	// Unique to Data Payload
	TargetId      string `json:"targetId"`
	HPointer      string `json:"hPointer"`
	BZCertHash    string `json:"bZCertHash"`
	ActionPayload []byte `json:"actionPayload"`
}

func (d DataPayload) BuildResponsePayload(actionPayload []byte, pubKey string, schemaVersion string) (DataAckPayload, error) {
	hashBytes, _ := util.HashPayload(d)
	hash := base64.StdEncoding.EncodeToString(hashBytes)

	return DataAckPayload{
		SchemaVersion:         schemaVersion,
		Type:                  string(DataAck),
		Action:                d.Action,
		TargetPublicKey:       pubKey,
		HPointer:              hash,
		ActionResponsePayload: actionPayload,
	}, nil
}
