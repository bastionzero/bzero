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
	"bastionzero.com/bctl/v1/bzerolib/logger"
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
	logger *logger.Logger
	// hPointer         string
	// expectedHPointer string
	clientPubKey    string
	clientSecretKey string
	bzcertHash      string

	agentPubKey  string
	ackPublicKey string

	// for grabbing and updating id tokens
	zliConfigPath          string
	zliRefreshTokenCommand string

	// ordered hash map to keep track of sent keysplitting messages
	pipelineMap   *orderedmap.OrderedMap
	pipelineQueue chan *ksmsg.KeysplittingMessage
	pipelineLock  sync.Mutex // no precalculation when syn's in flight

	// not the last ack we've received but the last ack we've received *in order*
	lastAck        *ksmsg.KeysplittingMessage
	outOfOrderAcks map[string]*ksmsg.KeysplittingMessage
}

func New(
	logger *logger.Logger,
	agentPubKey string,
	configPath string,
	refreshTokenCommand string,
) (*Keysplitting, error) {

	// TODO: load keys from storage
	keysplitter := &Keysplitting{
		logger:                 logger,
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

func (k *Keysplitting) Recover(errMessage rrr.ErrorMessage) error {
	// only recover from this error message if it corresponds to a message we've actually sent
	if errMessage.HPointer == "" {
		return fmt.Errorf("error message hpointer empty, not recovering")
	} else if pair := k.pipelineMap.GetPair(errMessage.HPointer); pair == nil {
		return fmt.Errorf("agent error is not on a message sent by this datachannel")
	} else if k.lastAck == nil {
		return fmt.Errorf("daemon cannot recover from an error on initial syn")
	}

	if syn, err := k.BuildSyn("", []byte{}); err != nil {
		return err
	} else {
		k.pipelineQueue <- &syn
		return nil
	}
}

func (k *Keysplitting) resend(hpointer string) {
	// figure out where we need to start resending from
	if pair := k.pipelineMap.GetPair(hpointer); pair == nil {

		// if the referenced message was acked, we won't have it in our map so we assume we
		// have to resend everything
		for lostPair := k.pipelineMap.Oldest(); lostPair != nil; lostPair.Next() {
			ksMessage := lostPair.Value.(ksmsg.KeysplittingMessage)
			k.pipeline(ksMessage.GetAction(), ksMessage.GetActionPayload())
		}
	} else {

		// if the hpointer references a message that hasn't been acked, we assume the ack
		// dropped and resend all messages starting with the one immediately AFTER the one
		// referenced by the hpointer
		for lostPair := pair.Next(); lostPair != nil; lostPair.Next() {
			ksMessage := lostPair.Value.(ksmsg.KeysplittingMessage)
			k.pipeline(ksMessage.GetAction(), ksMessage.GetActionPayload())
		}
	}

	// empty out our pipeline map
	k.pipelineMap = orderedmap.New()
	k.pipelineLock.Unlock()
}

func (k *Keysplitting) Validate(ksMessage *ksmsg.KeysplittingMessage) error {
	// TODO: CWC-1553: Remove this code once all agents have updated
	switch msg := ksMessage.KeysplittingPayload.(type) {
	case ksmsg.SynAckPayload:
		if k.ackPublicKey == "" {
			k.ackPublicKey = msg.TargetPublicKey
		}
	}

	// Verify the agent's signature
	if err := ksMessage.VerifySignature(k.agentPubKey); err != nil {
		// TODO: CWC-1553: Remove this inner conditional once all agents have updated
		if innerErr := ksMessage.VerifySignature(k.ackPublicKey); innerErr != nil {
			return fmt.Errorf("failed to verify %v signature: inner error: %v. original error: %v", ksMessage.Type, innerErr, err)
		}
	}

	// Check this messages is in response to one we've sent
	if hpointer, err := ksMessage.GetHpointer(); err != nil {
		return err
	} else if _, ok := k.pipelineMap.Get(hpointer); ok {
		if pair := k.pipelineMap.Oldest(); pair == nil {
			return fmt.Errorf("where did this ack come from?! we're not waiting for a response to any messages")
		} else if pair.Key != hpointer {
			if len(k.outOfOrderAcks) > pipelineLimit {
				// we're missing an ack sometime in the past, let's try to recover
				if syn, err := k.BuildSyn("", []byte{}); err != nil {
					return fmt.Errorf("could not recover from missing ack: %s", err)
				} else {
					k.pipelineQueue <- &syn
					return fmt.Errorf("hold up, we're missing an ack")
				}
			}
			k.outOfOrderAcks[hpointer] = ksMessage
		} else {
			k.lastAck = ksMessage
			k.pipelineMap.Delete(hpointer)
			k.processOutOfOrderAcks()

			if ksMessage.Type == ksmsg.SynAck {
				msg := ksMessage.KeysplittingPayload.(ksmsg.SynAckPayload)
				k.resend(msg.Nonce)
			}
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

	if action == "" {
		return fmt.Errorf("can't build keysplitting message with empty action")
	}

	// get the ack we're going to be building our new message off of
	if pair := k.pipelineMap.Newest(); pair == nil {

		// if our pipeline map is empty, we build off our last received ack
		if k.lastAck != nil {
			ack = k.lastAck
		} else {
			return fmt.Errorf("can't build message because there's nothing to build it off of")
		}
	} else {

		// otherwise, we're going to need to predict the ack we're building off of
		ksMessage := pair.Value.(ksmsg.KeysplittingMessage)
		if newAck, _, err := ksMessage.BuildUnsignedAck([]byte{}, k.agentPubKey); err != nil {
			return fmt.Errorf("failed to predict ack: %s", err)
		} else {
			ack = &newAck
		}
	}

	// build our new data message and then ship it!
	if newMessage, err := k.buildResponse(ack, action, actionPayload); err != nil {
		return fmt.Errorf("failed to build new message: %s", err)
	} else if err := k.addToPipelineMap(newMessage); err != nil {
		return err
	} else {
		k.pipelineQueue <- &newMessage
		return nil
	}
}

func (k *Keysplitting) buildResponse(ksMessage *ksmsg.KeysplittingMessage, action string, actionPayload []byte) (ksmsg.KeysplittingMessage, error) {
	if responseMessage, _, err := ksMessage.BuildUnsignedResponse(action, actionPayload, k.bzcertHash); err != nil {
		return responseMessage, err
	} else if err := responseMessage.Sign(k.clientSecretKey); err != nil {
		return responseMessage, fmt.Errorf("could not sign payload: %s", err)
	} else {
		return responseMessage, nil
	}
}

func (k *Keysplitting) addToPipelineMap(ksMessage ksmsg.KeysplittingMessage) error {
	if hashBytes, ok := util.HashPayload(ksMessage.KeysplittingPayload); !ok {
		return fmt.Errorf("failed to hash message")
	} else {
		hash := base64.StdEncoding.EncodeToString(hashBytes)
		k.pipelineMap.Set(hash, ksMessage)
		k.logger.Infof("WE'VE ADDED A NEW %s MESSAGE TO OUR OUTPUT STUFF", ksMessage.Type)
		return nil
	}
}

func (k *Keysplitting) BuildSyn(action string, payload []byte) (ksmsg.KeysplittingMessage, error) {
	// lock our pipeline because nothing can be calculated until we get our synack
	k.pipelineLock.Lock()

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
		Nonce:         util.Nonce(),
		BZCert:        bzCert,
	}

	ksMessage := ksmsg.KeysplittingMessage{
		Type:                ksmsg.Syn,
		KeysplittingPayload: synPayload,
	}

	// Sign it and add it to our hash map
	if err := ksMessage.Sign(k.clientSecretKey); err != nil {
		return ksmsg.KeysplittingMessage{}, fmt.Errorf("could not sign payload: %s", err)
	} else if err := k.addToPipelineMap(ksMessage); err != nil {
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
