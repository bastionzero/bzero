package message

import (
	ed "crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"

	"bastionzero.com/bctl/v1/bzerolib/keysplitting/util"
)

// Type restrictions for keysplitting messages
type KeysplittingPayloadType string

const (
	Syn     KeysplittingPayloadType = "Syn"
	SynAck  KeysplittingPayloadType = "SynAck"
	Data    KeysplittingPayloadType = "Data"
	DataAck KeysplittingPayloadType = "DataAck"
)

const (
	SchemaVersion = "1.1"
)

// type IKeysplittingPayload interface {
// 	GetHpointer() (string, error)
// 	GetAction() string
// 	GetActionPayload() []byte
// }

// type IKeysplittingMessage interface {
// 	BuildResponse(actionPayload interface{}, publickey string) (KeysplittingMessage, error)
// 	VerifySignature(publicKey string) error
// 	Sign(privateKey string) error
// }

type KeysplittingMessage struct {
	Timestamp           int64                   `json:"timestamp"`
	Type                KeysplittingPayloadType `json:"type"`
	KeysplittingPayload interface{}             `json:"keysplittingPayload"`
	Signature           string                  `json:"signature"`
}

func (k *KeysplittingMessage) Hash() string {
	// grab the hash of the keysplitting message
	if hashBytes, ok := util.HashPayload(k.KeysplittingPayload); !ok {
		return ""
	} else {
		return base64.StdEncoding.EncodeToString(hashBytes)
	}
}

func (k *KeysplittingMessage) BuildUnsignedAck(payload []byte, pubKey string) (KeysplittingMessage, error) {
	switch msg := k.KeysplittingPayload.(type) {
	case SynPayload:
		if synAckPayload, err := msg.BuildResponsePayload(payload, pubKey); err != nil {
			return KeysplittingMessage{}, err
		} else {
			return KeysplittingMessage{
				Timestamp:           time.Now().Unix(),
				Type:                SynAck,
				KeysplittingPayload: synAckPayload,
			}, nil
		}
	case DataPayload:
		if dataAckPayload, err := msg.BuildResponsePayload(payload, pubKey); err != nil {
			return KeysplittingMessage{}, err
		} else {
			return KeysplittingMessage{
				Timestamp:           time.Now().Unix(),
				Type:                DataAck,
				KeysplittingPayload: dataAckPayload,
			}, nil
		}
	default:
		return KeysplittingMessage{}, fmt.Errorf("can't build ack for message type: %T", k.KeysplittingPayload)
	}
}

func (k *KeysplittingMessage) BuildUnsignedResponse(action string, actionPayload []byte, bzcertHash string) (KeysplittingMessage, error) {
	switch msg := k.KeysplittingPayload.(type) {
	case SynAckPayload:
		if dataPayload, err := msg.BuildResponsePayload(action, actionPayload, bzcertHash); err != nil {
			return KeysplittingMessage{}, err
		} else {
			return KeysplittingMessage{
				Timestamp:           time.Now().Unix(),
				Type:                Data,
				KeysplittingPayload: dataPayload,
			}, nil
		}
	case DataAckPayload:
		if dataPayload, err := msg.BuildResponsePayload(action, actionPayload, bzcertHash); err != nil {
			return KeysplittingMessage{}, err
		} else {
			return KeysplittingMessage{
				Timestamp:           time.Now().Unix(),
				Type:                Data,
				KeysplittingPayload: dataPayload,
			}, nil
		}
	default:
		return KeysplittingMessage{}, fmt.Errorf("can't build responses for message type: %T", k.KeysplittingPayload)
	}
}

func (k *KeysplittingMessage) GetHpointer() (string, error) {
	switch msg := k.KeysplittingPayload.(type) {
	case SynPayload:
		return "", fmt.Errorf("syn payloads don't have hpointers")
	case SynAckPayload:
		return msg.HPointer, nil
	case DataPayload:
		return msg.HPointer, nil
	case DataAckPayload:
		return msg.HPointer, nil
	default:
		return "", fmt.Errorf("could not get hpointer for invalid keysplitting message type: %T", k.KeysplittingPayload)
	}
}

func (k *KeysplittingMessage) GetAction() string {
	switch msg := k.KeysplittingPayload.(type) {
	case SynPayload:
		return msg.Action
	case SynAckPayload:
		return msg.Action
	case DataPayload:
		return msg.Action
	case DataAckPayload:
		return msg.Action
	default:
		return ""
	}
}

func (k *KeysplittingMessage) GetActionPayload() []byte {
	switch msg := k.KeysplittingPayload.(type) {
	case SynPayload:
		return msg.ActionPayload
	case SynAckPayload:
		return msg.ActionResponsePayload
	case DataPayload:
		return msg.ActionPayload
	case DataAckPayload:
		return msg.ActionResponsePayload
	default:
		return []byte{}
	}
}

func (k *KeysplittingMessage) VerifySignature(publicKey string) error {
	pubKeyBits, _ := base64.StdEncoding.DecodeString(publicKey)
	if len(pubKeyBits) != 32 {
		return fmt.Errorf("public key has invalid length %v", len(pubKeyBits))
	}
	pubkey := ed.PublicKey(pubKeyBits)

	hashBits, ok := util.HashPayload(k.KeysplittingPayload)
	if !ok {
		return fmt.Errorf("failed to hash the keysplitting payload")
	}

	sigBits, _ := base64.StdEncoding.DecodeString(k.Signature)

	//log.Printf("\npubkey: %v\nhash: %v\nsignature: %v", publicKey, string(hashBits), k.Signature)

	if ok := ed.Verify(pubkey, hashBits, sigBits); ok {
		return nil
	} else {
		return fmt.Errorf("invalid signature: signature: %s payload: %+v", k.Signature, k.KeysplittingPayload)
	}
}

func (k *KeysplittingMessage) Sign(privateKey string) error {
	keyBytes, _ := base64.StdEncoding.DecodeString(privateKey)
	if len(keyBytes) != 64 {
		return fmt.Errorf("invalid private key length: %v", len(keyBytes))
	}
	privkey := ed.PrivateKey(keyBytes)

	hashBits, _ := util.HashPayload(k.KeysplittingPayload)

	sig := ed.Sign(privkey, hashBits)
	k.Signature = base64.StdEncoding.EncodeToString(sig)

	return nil
}

func (k *KeysplittingMessage) UnmarshalJSON(data []byte) error {
	var objmap map[string]*json.RawMessage

	if err := json.Unmarshal(data, &objmap); err != nil {
		return err
	}

	var t, s string
	if err := json.Unmarshal(*objmap["type"], &t); err != nil {
		return err
	} else {
		k.Type = KeysplittingPayloadType(t)
	}

	if err := json.Unmarshal(*objmap["signature"], &s); err != nil {
		return err
	} else {
		k.Signature = s
	}

	kPayload := *objmap["keysplittingPayload"]
	switch k.Type {
	case Syn:
		var synPayload SynPayload
		if err := json.Unmarshal(kPayload, &synPayload); err != nil {
			return fmt.Errorf("malformed Syn Payload")
		} else {
			k.KeysplittingPayload = synPayload
		}
	case SynAck:
		var synAckPayload SynAckPayload
		if err := json.Unmarshal(kPayload, &synAckPayload); err != nil {
			return fmt.Errorf("malformed SynAck Payload")
		} else {
			k.KeysplittingPayload = synAckPayload
		}
	case Data:
		var dataPayload DataPayload
		if err := json.Unmarshal(kPayload, &dataPayload); err != nil {
			return fmt.Errorf("malformed Data Payload")
		} else {
			k.KeysplittingPayload = dataPayload
		}
	case DataAck:
		var dataAckPayload DataAckPayload
		if err := json.Unmarshal(kPayload, &dataAckPayload); err != nil {
			return fmt.Errorf("malformed DataAck Payload")
		} else {
			k.KeysplittingPayload = dataAckPayload
		}
	default:
		// TODO: explicitly check type of outer vs. inner payload
		return fmt.Errorf("type mismatch in keysplitting message and actual message payload")
	}

	return nil
}
