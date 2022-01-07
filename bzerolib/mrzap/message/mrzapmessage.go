package message

import (
	ed "crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"

	"bastionzero.com/bctl/v1/bzerolib/mrzap/util"
)

// Type restrictions for mrzap messages
type MrZAPPayloadType string

const (
	Syn     MrZAPPayloadType = "Syn"
	SynAck  MrZAPPayloadType = "SynAck"
	Data    MrZAPPayloadType = "Data"
	DataAck MrZAPPayloadType = "DataAck"
)

const (
	SchemaVersion = "1.0"
)

type IMrZAPMessage interface {
	BuildResponse(actionPayload interface{}, publickey string) (MrZAPMessage, error)
	VerifySignature(publicKey string) error
	Sign(privateKey string) error
}

type MrZAPMessage struct {
	Type         MrZAPPayloadType `json:"type"`
	MrZAPPayload interface{}      `json:"mrZAPPayload"`
	Signature    string           `json:"signature"`
}

func (m *MrZAPMessage) VerifySignature(publicKey string) error {
	pubKeyBits, _ := base64.StdEncoding.DecodeString(publicKey)
	if len(pubKeyBits) != 32 {
		return fmt.Errorf("public key has invalid length %v", len(pubKeyBits))
	}
	pubkey := ed.PublicKey(pubKeyBits)

	hashBits, ok := util.HashPayload(m.MrZAPPayload)
	if !ok {
		return fmt.Errorf("could not hash the mrzap payload")
	}

	sigBits, _ := base64.StdEncoding.DecodeString(m.Signature)

	//log.Printf("\npubkey: %v\nhash: %v\nsignature: %v", publicKey, string(hashBits), m.Signature)

	if ok := ed.Verify(pubkey, hashBits, sigBits); ok {
		return nil
	} else {
		return fmt.Errorf("failed to verify signature")
	}
}

func (m *MrZAPMessage) Sign(privateKey string) error {
	keyBytes, _ := base64.StdEncoding.DecodeString(privateKey)
	if len(keyBytes) != 64 {
		return fmt.Errorf("invalid private key length: %v", len(keyBytes))
	}
	privkey := ed.PrivateKey(keyBytes)

	hashBits, _ := util.HashPayload(m.MrZAPPayload)

	sig := ed.Sign(privkey, hashBits)
	m.Signature = base64.StdEncoding.EncodeToString(sig)

	return nil
}

func (m *MrZAPMessage) UnmarshalJSON(data []byte) error {
	var objmap map[string]*json.RawMessage

	if err := json.Unmarshal(data, &objmap); err != nil {
		return err
	}

	var t, s string
	if err := json.Unmarshal(*objmap["type"], &t); err != nil {
		return err
	} else {
		m.Type = MrZAPPayloadType(t)
	}

	if err := json.Unmarshal(*objmap["signature"], &s); err != nil {
		return err
	} else {
		m.Signature = s
	}

	kPayload := *objmap["mrZAPPayload"]
	switch m.Type {
	case Syn:
		var synPayload SynPayload
		if err := json.Unmarshal(kPayload, &synPayload); err != nil {
			return fmt.Errorf("malformed Syn Payload")
		} else {
			m.MrZAPPayload = synPayload
		}
	case SynAck:
		var synAckPayload SynAckPayload
		if err := json.Unmarshal(kPayload, &synAckPayload); err != nil {
			return fmt.Errorf("malformed SynAck Payload")
		} else {
			m.MrZAPPayload = synAckPayload
		}
	case Data:
		var dataPayload DataPayload
		if err := json.Unmarshal(kPayload, &dataPayload); err != nil {
			return fmt.Errorf("malformed Data Payload")
		} else {
			m.MrZAPPayload = dataPayload
		}
	case DataAck:
		var dataAckPayload DataAckPayload
		if err := json.Unmarshal(kPayload, &dataAckPayload); err != nil {
			return fmt.Errorf("malformed DataAck Payload")
		} else {
			m.MrZAPPayload = dataAckPayload
		}
	default:
		// TODO: explicitly check type of outer vs. inner payload
		return fmt.Errorf("type mismatch in mrzap message and actual message payload")
	}

	return nil
}
