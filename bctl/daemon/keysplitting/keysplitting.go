package keysplitting

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"sync"
	"time"

	orderedmap "github.com/wk8/go-ordered-map"

	rrr "bastionzero.com/bctl/v1/bzerolib/error"
	bzcrt "bastionzero.com/bctl/v1/bzerolib/keysplitting/bzcert"
	ksmsg "bastionzero.com/bctl/v1/bzerolib/keysplitting/message"
	"bastionzero.com/bctl/v1/bzerolib/keysplitting/util"
	"bastionzero.com/bctl/v1/bzerolib/plugin"
)

const (
	// the number of messages we're allowed to precalculate and send without having
	// received an ack
	pipelineLimit = 8
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
	pipelineMap    *orderedmap.OrderedMap
	pipelineQueue  chan *ksmsg.KeysplittingMessage
	pipelineLock   sync.Mutex
	outOfOrderAcks map[string]*ksmsg.KeysplittingMessage

	// all the messages we pre-calculated and now need to recalculate because there was an error
	recalculateQueue []*plugin.ActionWrapper

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
		pipelineMap:            orderedmap.New(),
		pipelineQueue:          make(chan *ksmsg.KeysplittingMessage, pipelineLimit),
		outOfOrderAcks:         make(map[string]*ksmsg.KeysplittingMessage),
	}

	return keysplitter, nil
}

func (k *Keysplitting) Outbox() <-chan *ksmsg.KeysplittingMessage {
	return k.pipelineQueue
}

// We can either recover from an hpointer or from a timestamp. This is because if we're recovering
// based on an error message, there may have been an error hashing the previous message but we should
// always at least have a timestamp.
func (k *Keysplitting) Recover(errMessage rrr.ErrorMessage) error {
	k.pipelineLock.Lock()

	if errMessage.HPointer != "" {
		for pair := k.pipelineMap.GetPair(errMessage.HPointer); pair != nil; pair.Next() {
			// add our messages to the recalculate queue so that when we get our syn/ack
			// we can pipeline the ones we have all over again
			ksMessage := pair.Value.(ksmsg.KeysplittingMessage)
			k.recalculateQueue = append(k.recalculateQueue, &plugin.ActionWrapper{
				Action:        ksMessage.GetAction(),
				ActionPayload: ksMessage.GetActionPayload(),
			})
		}
	}
	// else if timestamp != 0 {
	// 	errorTime := time.Unix(timestamp, 0)
	// 	for pair := k.outboxMap.Oldest(); pair != nil; pair.Next() {
	// 		// add our messages to the recalculate queue so that when we get our syn/ack
	// 		// we can pipeline the ones we have all over again
	// 		ksMessage := pair.Value.(ksmsg.KeysplittingMessage)
	// 		ksTime := time.Unix(ksMessage.GetTimestamp(), 0)
	// 		if ksTime.After(errorTime) {
	// 			k.recalculateQueue = append(k.recalculateQueue, &plugin.ActionWrapper{
	// 				Action:        ksMessage.GetAction(),
	// 				ActionPayload: ksMessage.GetActionPayload(),
	// 			})
	// 		}
	// 	}
	// }

	if msg, err := k.BuildSyn("", []byte{}); err != nil {
		return err
	} else {
		k.pipelineQueue <- &msg
		return nil
	}
}

func (k *Keysplitting) Validate(ksMessage *ksmsg.KeysplittingMessage) error {
	var hpointer string
	switch msg := ksMessage.KeysplittingPayload.(type) {
	case ksmsg.SynAckPayload:
		hpointer = msg.HPointer

		// TODO: CWC-1553: Remove this code once all agents have updated
		if k.ackPublicKey == "" {
			k.ackPublicKey = msg.TargetPublicKey
		}

		// as part of our recovery, we need to recalculate and resend all of our in limbo messages
		for _, wrapped := range k.recalculateQueue {
			k.pipeline(wrapped.Action, wrapped.ActionPayload)
		}

		k.pipelineLock.Unlock()
	case ksmsg.DataAckPayload:
		hpointer = msg.HPointer
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
	if _, ok := k.pipelineMap.Get(hpointer); ok {
		if pair := k.pipelineMap.Oldest(); pair != nil {
			return fmt.Errorf("where did this ack come from?! we're not waiting for a response to any messages")
		} else if pair.Key != hpointer {
			if len(k.outOfOrderAcks) > pipelineLimit {
				return fmt.Errorf("hold up, we're missing an ack") // LUCIE: RECOVER FROM THIS?
			}
			k.outOfOrderAcks[hpointer] = ksMessage
		} else {
			k.lastAck = ksMessage
			k.pipelineMap.Delete(hpointer)
			k.processOutOfOrderAcks()
		}
	} else {
		return fmt.Errorf("%T message did not correspond to a previously sent message", ksMessage.KeysplittingPayload)
	}

	return nil
}

func (k *Keysplitting) processOutOfOrderAcks() {
	for pair := k.pipelineMap.Oldest(); pair != nil; pair.Next() {
		if ack, ok := k.outOfOrderAcks[pair.Key.(string)]; !ok {
			return
		} else {
			k.lastAck = ack
			k.pipelineMap.Delete(pair.Key)
		}
	}
}

func (k *Keysplitting) Inbox(action string, actionPayload []byte) error {
	k.pipelineLock.Lock()
	defer k.pipelineLock.Unlock()
	return k.pipeline(action, actionPayload)
}

func (k *Keysplitting) pipeline(action string, actionPayload []byte) error {
	var ack *ksmsg.KeysplittingMessage

	// get the last message we sent
	if pair := k.pipelineMap.Newest(); pair != nil {
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
	} else if err := k.addToOutbox(newMessage); err != nil {
		return err
	} else {
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
		return responseMessage, fmt.Errorf("could not sign payload: %s", err)
	} else {
		return responseMessage, nil
	}
}

// func (k *Keysplitting) hashMessage(ksMessage *ksmsg.KeysplittingMessage) (string, error) {
// 	if hashBytes, ok := util.HashPayload(ksMessage.KeysplittingPayload); !ok {
// 		return "", fmt.Errorf("failed to hash message")
// 	} else {
// 		return base64.StdEncoding.EncodeToString(hashBytes), nil
// 	}
// }

func (k *Keysplitting) addToOutbox(ksMessage ksmsg.KeysplittingMessage) error {
	if hashBytes, ok := util.HashPayload(ksMessage.KeysplittingPayload); !ok {
		return fmt.Errorf("failed to hash message")
	} else {
		hash := base64.StdEncoding.EncodeToString(hashBytes)
		k.pipelineMap.Set(hash, ksMessage)
		k.pipelineQueue <- &ksMessage
		return nil
	}
}

func (k *Keysplitting) BuildSyn(action string, payload []byte) (ksmsg.KeysplittingMessage, error) {
	// lock our pipeline because nothing can be calculated until we get our synack
	k.pipelineLock.Lock()

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
		Timestamp:     time.Now().Unix(),
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
		return ksmsg.KeysplittingMessage{}, fmt.Errorf("could not sign payload: %s", err)
	} else if err := k.addToOutbox(ksMessage); err != nil {
		return ksmsg.KeysplittingMessage{}, err
	} else {
		return ksMessage, nil
	}

	// else if hash, err := k.hashMessage(&ksMessage); err != nil {
	// 	return ksmsg.KeysplittingMessage{}, fmt.Errorf("failed to hash syn")
	// } else {
	// 	k.outbox.Set(hash, ksMessage)
	// 	return ksMessage, nil
	// }

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
