package keysplitting

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"time"

	orderedmap "github.com/wk8/go-ordered-map"

	bzcrt "bastionzero.com/bctl/v1/bzerolib/keysplitting/bzcert"
	ksmsg "bastionzero.com/bctl/v1/bzerolib/keysplitting/message"
	"bastionzero.com/bctl/v1/bzerolib/keysplitting/util"
)

type ZLIConfig struct {
	KSConfig KeysplittingConfig `json:"keySplitting"`
	TokenSet TokenSetConfig     `json:"tokenSet"`
}
type TokenSetConfig struct {
	CurrentIdToken string `json:"id_token"`
}

type KeysplittingConfig struct {
	PrivateKey       string `json:"privateKey"`
	PublicKey        string `json:"publicKey"`
	CerRand          string `json:"cerRand"`
	CerRandSignature string `json:"cerRandSig"`
	InitialIdToken   string `json:"initialIdToken"`
}

type Keysplitting struct {
	// hPointer         string
	// expectedHPointer string
	clientPubKey    string
	clientSecretKey string
	bzcertHash      string

	zliConfigPath          string
	zliRefreshTokenCommand string

	agentPubKey  string
	ackPublicKey string

	// ordered hash map to keep track of sent keysplitting messages
	outbox         *orderedmap.OrderedMap
	outOfOrderAcks map[string]*ksmsg.KeysplittingMessage

	// not the last ack we've received but the last ack we've received *in order*
	lastAck *ksmsg.KeysplittingMessage
}

func New(
	agentPubKey string,
	configPath string,
	refreshTokenCommand string,
) (*Keysplitting, error) {

	// TODO: load keys from storage
	keysplitter := &Keysplitting{
		zliConfigPath:          configPath,
		zliRefreshTokenCommand: refreshTokenCommand,
		agentPubKey:            agentPubKey,
		ackPublicKey:           "",
		outbox:                 orderedmap.New(),
		outOfOrderAcks:         make(map[string]*ksmsg.KeysplittingMessage),
	}

	return keysplitter, nil
}

func (k *Keysplitting) Validate(ksMessage *ksmsg.KeysplittingMessage) error {
	var hpointer string
	switch ksMessage.Type {
	case ksmsg.SynAck:
		synAckPayload := ksMessage.KeysplittingPayload.(ksmsg.SynAckPayload)
		hpointer = synAckPayload.HPointer

		// TODO: CWC-1553: Remove this code once all agents have updated
		if k.ackPublicKey == "" {
			k.ackPublicKey = synAckPayload.TargetPublicKey
		}
	case ksmsg.DataAck:
		dataAckPayload := ksMessage.KeysplittingPayload.(ksmsg.DataAckPayload)
		hpointer = dataAckPayload.HPointer
	default:
		return fmt.Errorf("error validating unhandled Keysplitting type")
	}

	// Verify the agent's signature
	if err := ksMessage.VerifySignature(k.agentPubKey); err != nil {
		// TODO: CWC-1553: Remove this inner conditional once all agents have updated
		if innerErr := ksMessage.VerifySignature(k.ackPublicKey); innerErr != nil {
			return fmt.Errorf("failed to verify %v signature: inner error: %v. original error: %v", ksMessage.Type, innerErr, err)
		}
	}

	// Check this messages is in response to one we've sent
	if _, ok := k.outbox.Get(hpointer); ok {
		if pair := k.outbox.Oldest(); pair != nil {
			return fmt.Errorf("where did this ack come from?! we're not waiting for a response to any messages")
		} else if pair.Key != hpointer {
			k.outOfOrderAcks[hpointer] = ksMessage
		} else {
			k.outbox.Delete(hpointer)
			k.lastAck = ksMessage
		}
	} else {
		return fmt.Errorf("%T message did not correspond to a previously sent message", ksMessage.KeysplittingPayload)
	}

	return nil
}

func (k *Keysplitting) Pipeline(action string, actionPayload []byte) error {
	var ack *ksmsg.KeysplittingMessage

	// get the last message we sent
	if pair := k.outbox.Newest(); pair != nil {
		// if there is none, then we're building off our last ack
		if k.lastAck != nil {
			ack = k.lastAck
		} else {
			return fmt.Errorf("can't build message because there's nothing to build it off of")
		}
	} else {
		// predict the ack of our most recently sent message
		ksMessage := pair.Value.(*ksmsg.KeysplittingMessage)
		if newAck, err := k.predictAck(ksMessage); err != nil {
			return fmt.Errorf("failed to predict ack: %s", err)
		} else {
			ack = &newAck
		}
	}

	if newMessage, err := k.buildResponse(ack, action, actionPayload); err != nil {
		return fmt.Errorf("failed to build new message: %s", err)
	} else if hash, err := k.hashMessage(&newMessage); err != nil {
		return fmt.Errorf("could not pipeline message because we couldn't hash our predicted response")
	} else {
		k.outbox.Set(hash, newMessage)
		return nil
	}
}

func (k *Keysplitting) predictAck(ksMessage *ksmsg.KeysplittingMessage) (ksmsg.KeysplittingMessage, error) {
	switch msg := ksMessage.KeysplittingPayload.(type) {
	case ksmsg.SynPayload:
		if synAckPayload, _, err := msg.BuildResponsePayload([]byte{}, k.agentPubKey); err != nil {
			return ksmsg.KeysplittingMessage{}, err
		} else {
			return ksmsg.KeysplittingMessage{
				Type:                ksmsg.SynAck,
				KeysplittingPayload: synAckPayload,
			}, nil
		}
	case ksmsg.DataPayload:
		if dataAckPayload, _, err := msg.BuildResponsePayload([]byte{}, k.agentPubKey); err != nil {
			return ksmsg.KeysplittingMessage{}, err
		} else {
			return ksmsg.KeysplittingMessage{
				Type:                ksmsg.DataAck,
				KeysplittingPayload: dataAckPayload,
			}, nil
		}
	default:
		return ksmsg.KeysplittingMessage{}, fmt.Errorf("can't predict acks for message type: %T", ksMessage.KeysplittingPayload)
	}
}

func (k *Keysplitting) buildResponse(ksMessage *ksmsg.KeysplittingMessage, action string, actionPayload []byte) (ksmsg.KeysplittingMessage, error) {
	var responseMessage ksmsg.KeysplittingMessage

	switch msg := ksMessage.KeysplittingPayload.(type) {
	case ksmsg.SynAckPayload:
		if dataPayload, _, err := msg.BuildResponsePayload(action, actionPayload, k.bzcertHash); err != nil {
			return ksmsg.KeysplittingMessage{}, err
		} else {
			responseMessage = ksmsg.KeysplittingMessage{
				Type:                ksmsg.Data,
				KeysplittingPayload: dataPayload,
			}
		}
	case ksmsg.DataAckPayload:
		if dataPayload, _, err := msg.BuildResponsePayload(action, actionPayload, k.bzcertHash); err != nil {
			return ksmsg.KeysplittingMessage{}, err
		} else {
			responseMessage = ksmsg.KeysplittingMessage{
				Type:                ksmsg.Data,
				KeysplittingPayload: dataPayload,
			}
		}
	}

	if err := responseMessage.Sign(k.clientSecretKey); err != nil {
		return responseMessage, fmt.Errorf("could not sign payload: %v", err.Error())
	} else {
		return responseMessage, nil
	}
}

func (k *Keysplitting) hashMessage(ksMessage *ksmsg.KeysplittingMessage) (string, error) {
	if hashBytes, ok := util.HashPayload(ksMessage.KeysplittingPayload); !ok {
		return "", fmt.Errorf("failed to hash message")
	} else {
		return base64.StdEncoding.EncodeToString(hashBytes), nil
	}
}

func (k *Keysplitting) BuildSyn(action string, payload []byte) (ksmsg.KeysplittingMessage, error) {
	// If this is the beginning of the hash chain, then we create a nonce with a random value,
	// otherwise we use the hash of the previous value to maintain the hash chain and immutability
	nonce := util.Nonce()
	if k.lastAck != nil {
		if hpointer, err := k.lastAck.GetHpointer(); err != nil {
			return ksmsg.KeysplittingMessage{}, fmt.Errorf("failed to get hpointer of last ack: %s", err)
		} else {
			nonce = hpointer
		}
	}

	// Build the BZero Certificate then store hash for future messages
	bzCert, err := k.buildBZCert()
	if err != nil {
		return ksmsg.KeysplittingMessage{}, fmt.Errorf("error building bzecert: %v", err.Error())
	} else {
		if hash, ok := bzCert.Hash(); ok {
			k.bzcertHash = hash
		} else {
			return ksmsg.KeysplittingMessage{}, fmt.Errorf("could not hash BZ Certificate")
		}
	}

	// Build the keysplitting message
	synPayload := ksmsg.SynPayload{
		Timestamp:     fmt.Sprint(time.Now().Unix()),
		SchemaVersion: ksmsg.SchemaVersion,
		Type:          string(ksmsg.Syn),
		Action:        action,
		ActionPayload: payload,
		TargetId:      k.agentPubKey,
		Nonce:         nonce,
		BZCert:        bzCert,
	}

	ksMessage := ksmsg.KeysplittingMessage{
		Type:                ksmsg.Syn,
		KeysplittingPayload: synPayload,
	}

	// Sign it and add it to our hash map
	if err := ksMessage.Sign(k.clientSecretKey); err != nil {
		return ksMessage, fmt.Errorf("could not sign payload: %v", err.Error())
	} else if hash, err := k.hashMessage(&ksMessage); err != nil {
		return ksmsg.KeysplittingMessage{}, fmt.Errorf("failed to hash syn")
	} else {
		k.outbox.Set(hash, ksMessage)
		return ksMessage, nil
	}

	// hashBytes, _ := util.HashPayload(synPayload)
	// k.expectedHPointer = base64.StdEncoding.EncodeToString(hashBytes)
	// return ksMessage, nil
}

func (k *Keysplitting) buildBZCert() (bzcrt.BZCert, error) {
	// update the id token by calling the passed in zli command
	if err := util.RunRefreshAuthCommand(k.zliRefreshTokenCommand); err != nil {
		return bzcrt.BZCert{}, err
	} else if zliConfig, err := k.loadZLIConfig(); err != nil {
		return bzcrt.BZCert{}, err
	} else {
		// Set public and private keys because someone maybe have logged out and logged back in again
		k.clientPubKey = zliConfig.KSConfig.PublicKey

		// The golang ed25519 library uses a length 64 private key because the private key is the concatenated form
		// privatekey = privatekey + publickey.  So if it was generated as length 32, we can correct for that here
		if privatekeyBytes, _ := base64.StdEncoding.DecodeString(zliConfig.KSConfig.PrivateKey); len(privatekeyBytes) == 32 {
			publickeyBytes, _ := base64.StdEncoding.DecodeString(k.clientPubKey)
			k.clientSecretKey = base64.StdEncoding.EncodeToString(append(privatekeyBytes, publickeyBytes...))
		} else {
			k.clientSecretKey = zliConfig.KSConfig.PrivateKey
		}

		return bzcrt.BZCert{
			InitialIdToken:  zliConfig.KSConfig.InitialIdToken,
			CurrentIdToken:  zliConfig.TokenSet.CurrentIdToken,
			ClientPublicKey: zliConfig.KSConfig.PublicKey,
			Rand:            zliConfig.KSConfig.CerRand,
			SignatureOnRand: zliConfig.KSConfig.CerRandSignature,
		}, nil
	}
}

func (k *Keysplitting) loadZLIConfig() (*ZLIConfig, error) {
	var config ZLIConfig

	if configFile, err := os.Open(k.zliConfigPath); err != nil {
		return nil, fmt.Errorf("could not open config file: %v", err.Error())
	} else if configFileBytes, err := ioutil.ReadAll(configFile); err != nil {
		return nil, fmt.Errorf("failed to read config: %s", err)
	} else if err := json.Unmarshal(configFileBytes, &config); err != nil {
		return nil, fmt.Errorf("could not unmarshal config file")
	} else {
		return &config, nil
	}
}
