package message

import (
	"encoding/base64"

	"bastionzero.com/bctl/v1/bzerolib/keysplitting/util"
)

type DataAckPayload struct {
	SchemaVersion string `json:"schemaVersion"`
	Type          string `json:"type"`
	Action        string `json:"action"`

	// Unique to DataAck Payload
	TargetPublicKey       string `json:"targetPublicKey"`
	HPointer              string `json:"hPointer"`
	ActionResponsePayload []byte `json:"actionResponsePayload"`
}

func (d DataAckPayload) BuildResponsePayload(action string, actionPayload []byte, bzCertHash string) (DataPayload, error) {
	hashBytes, _ := util.HashPayload(d)
	hash := base64.StdEncoding.EncodeToString(hashBytes)

	return DataPayload{
		SchemaVersion: SchemaVersion,
		Type:          string(Data),
		Action:        action,
		TargetId:      d.TargetPublicKey, //TODO: Make this come from storage
		HPointer:      hash,
		ActionPayload: actionPayload,
		BZCertHash:    bzCertHash, // TODO: Make this come from storage
	}, nil
}
