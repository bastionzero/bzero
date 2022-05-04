package keysplitting

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"sync"

	"github.com/Masterminds/semver"
	orderedmap "github.com/wk8/go-ordered-map"

	rrr "bastionzero.com/bctl/v1/bzerolib/error"
	bzcrt "bastionzero.com/bctl/v1/bzerolib/keysplitting/bzcert"
	ksmsg "bastionzero.com/bctl/v1/bzerolib/keysplitting/message"
	"bastionzero.com/bctl/v1/bzerolib/keysplitting/util"
	"bastionzero.com/bctl/v1/bzerolib/logger"
)

// the number of messages we're allowed to precalculate and send without having
// received an ack
var pipelineLimit = 8

type ZLIConfig struct {
	KSConfig KeysplittingConfig `json:"keySplitting"`
	TokenSet ZLITokenSetConfig  `json:"tokenSet"`
}
type ZLITokenSetConfig struct {
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

	clientPubKey    string
	clientSecretKey string
	bzcertHash      string

	agentPubKey  string
	ackPublicKey string

	// for grabbing and updating id tokens
	zliConfigPath          string
	zliRefreshTokenCommand string

	// ordered hash map to keep track of sent keysplitting messages
	pipelineMap  *orderedmap.OrderedMap
	pipelineLock sync.Mutex
	pipelineOpen *sync.Cond

	// a channel for all the messages we give the datachannel to send
	outboxQueue chan *ksmsg.KeysplittingMessage

	// not the last ack we've received but the last ack we've received *in order*
	lastAck        *ksmsg.KeysplittingMessage
	outOfOrderAcks map[string]*ksmsg.KeysplittingMessage

	// bool variable for letting the datachannel know when to start processing incoming messages again
	recovering bool

	// we need to know the version the agent is using so we can do icky things
	// TODO: CWC-1820: remove once all agents have updated
	prePipeliningAgent bool
	synAction          string
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
		outboxQueue:            make(chan *ksmsg.KeysplittingMessage, pipelineLimit),
		outOfOrderAcks:         make(map[string]*ksmsg.KeysplittingMessage),
		recovering:             false,
		synAction:              "initial",
	}
	keysplitter.pipelineOpen = sync.NewCond(&keysplitter.pipelineLock)

	return keysplitter, nil
}

func (k *Keysplitting) Recovering() bool {
	return k.recovering
}

func (k *Keysplitting) Release() {
	k.pipelineOpen.Broadcast()
}

func (k *Keysplitting) Outbox() <-chan *ksmsg.KeysplittingMessage {
	return k.outboxQueue
}

func (k *Keysplitting) Recover(errMessage rrr.ErrorMessage) error {
	// only recover from this error message if it corresponds to a message we've actually sent
	// our old error messages weren't setting hpointers correctly
	// TODO: CWC-1818: remove schema version check
	if errMessage.SchemaVersion != "" {
		if errMessage.HPointer == "" {
			return fmt.Errorf("error message hpointer empty")
		} else if pair := k.pipelineMap.GetPair(errMessage.HPointer); pair == nil {
			return fmt.Errorf("agent error is not on a message sent by this datachannel")
		}
	}

	k.recovering = true
	if _, err := k.BuildSyn("", []byte{}, true); err != nil {
		return err
	}
	return nil
}

func (k *Keysplitting) resend(hpointer string) {
	recoveryMap := *k.pipelineMap
	k.pipelineMap = orderedmap.New()

	// figure out where we need to start resending from
	if pair := (&recoveryMap).GetPair(hpointer); pair == nil {

		// if the referenced message was acked, we won't have it in our map so we assume we
		// have to resend everything
		for lostPair := (&recoveryMap).Oldest(); lostPair != nil; lostPair = lostPair.Next() {
			ksMessage := lostPair.Value.(ksmsg.KeysplittingMessage)
			k.pipeline(ksMessage.GetAction(), ksMessage.GetActionPayload())
		}
	} else {

		// if the hpointer references a message that hasn't been acked, we assume the ack
		// dropped and resend all messages starting with the one immediately AFTER the one
		// referenced by the hpointer
		for lostPair := pair.Next(); lostPair != nil; lostPair = lostPair.Next() {
			ksMessage := lostPair.Value.(ksmsg.KeysplittingMessage)
			k.pipeline(ksMessage.GetAction(), ksMessage.GetActionPayload())
		}
	}
}

func (k *Keysplitting) Validate(ksMessage *ksmsg.KeysplittingMessage) error {
	// TODO: CWC-1553: Remove this code once all agents have updated
	if msg, ok := ksMessage.KeysplittingPayload.(ksmsg.SynAckPayload); ok && k.ackPublicKey == "" {
		k.ackPublicKey = msg.TargetPublicKey
	}

	// Verify the agent's signature
	if err := ksMessage.VerifySignature(k.agentPubKey); err != nil {
		// TODO: CWC-1553: Remove this inner conditional once all agents have updated
		if innerErr := ksMessage.VerifySignature(k.ackPublicKey); innerErr != nil {
			return fmt.Errorf("failed to verify %v signature: inner error: %s. original error: %s", ksMessage.Type, innerErr, err)
		}
	}

	// Check this messages is in response to one we've sent
	if hpointer, err := ksMessage.GetHpointer(); err != nil {
		return err
	} else if _, ok := k.pipelineMap.Get(hpointer); ok {
		switch ksMessage.Type {
		case ksmsg.SynAck:
			if msg, ok := ksMessage.KeysplittingPayload.(ksmsg.SynAckPayload); ok {
				defer k.pipelineLock.Unlock()

				k.lastAck = ksMessage
				k.pipelineMap.Delete(hpointer) // delete syn from map

				// when we recover, we're recovering based on the nonce in the syn/ack because
				// it is an hpointer which refers to the agent's last recieved and validated message
				// aka it is the current state of the mrzap hash chain according to the agent and this
				// recovery mechanism allows us to sync our mrzap state to that
				k.resend(msg.Nonce)
				k.recovering = false

				// check to see if we're talking with an agent that's using pre-2.0 keysplitting because
				// we'll need to dirty the payload by adding extra quotes around it
				// TODO: CWC-1820: remove once all daemon's are updated
				if c, err := semver.NewConstraint("< 2.0"); err != nil {
					return fmt.Errorf("unable to create versioning constraint")
				} else if v, err := semver.NewVersion(msg.SchemaVersion); err != nil {
					return fmt.Errorf("unable to parse version")
				} else {
					k.prePipeliningAgent = c.Check(v)

					if k.prePipeliningAgent {
						pipelineLimit = 1
					}
				}
			}
		case ksmsg.DataAck:
			// check if incoming message corresponds to our most recently sent data
			if pair := k.pipelineMap.Oldest(); pair == nil {
				return fmt.Errorf("where did this ack come from?! we're not waiting for a response to any messages")
			} else if pair.Key != hpointer {
				k.logger.Info("Received an out-of-order ack message")
				if len(k.outOfOrderAcks) > pipelineLimit {
					// we're missing an ack sometime in the past, let's try to recover
					if _, err := k.BuildSyn("", []byte{}, true); err != nil {
						k.recovering = true
						return fmt.Errorf("could not recover from missing ack: %s", err)
					} else {
						return fmt.Errorf("hold up, we're missing an ack. Going into recovery")
					}
				}
				k.outOfOrderAcks[hpointer] = ksMessage
			} else {
				k.lastAck = ksMessage
				k.pipelineMap.Delete(hpointer)
				k.processOutOfOrderAcks()
				k.pipelineOpen.Broadcast()
			}
		}
	} else {
		return fmt.Errorf("%T message did not correspond to a previously sent message", ksMessage.KeysplittingPayload)
	}

	return nil
}

func (k *Keysplitting) processOutOfOrderAcks() {
	for pair := k.pipelineMap.Oldest(); pair != nil; pair = pair.Next() {
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
	if action == "" {
		return fmt.Errorf("i'm not allowed to build a keysplitting message with empty action")
	}

	// get the ack we're going to be building our new message off of
	var ack *ksmsg.KeysplittingMessage
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
		if newAck, err := ksMessage.BuildUnsignedAck([]byte{}, k.agentPubKey); err != nil {
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
		k.outboxQueue <- &newMessage
		return nil
	}
}

func (k *Keysplitting) buildResponse(ksMessage *ksmsg.KeysplittingMessage, action string, payload []byte) (ksmsg.KeysplittingMessage, error) {
	// payloadBytes, err := json.Marshal(payload)

	if k.prePipeliningAgent {
		// if we're talking with an old agent, then we have to add extra quotes
		// TODO: CWC-1820: remove once all daemon's are updated

		// sometimes go will extra marshal big things, but because we need to compensate for an old
		// extra marshaling bug on our part, we have to make sure that we are marshaling things the
		// correct number of times which means that we have to unmarshal the things that got extra
		// marshaled and then fancy marshal them in the special broken way we have to reproduce for
		// backwards compatability with old agents
		var preMarshal []byte
		if err := json.Unmarshal(payload, &preMarshal); err == nil {
			payload = preMarshal
		}

		encoded := base64.StdEncoding.EncodeToString(payload)
		payload, _ = json.Marshal(string(encoded))
	}

	if responseMessage, err := ksMessage.BuildUnsignedResponse(action, payload, k.bzcertHash); err != nil {
		return responseMessage, err
	} else if err := responseMessage.Sign(k.clientSecretKey); err != nil {
		return responseMessage, fmt.Errorf("could not sign payload: %s", err)
	} else {
		return responseMessage, nil
	}
}

func (k *Keysplitting) addToPipelineMap(ksMessage ksmsg.KeysplittingMessage) error {
	if hash := ksMessage.Hash(); hash == "" {
		return fmt.Errorf("failed to hash message")
	} else {
		// we only want to pipeline up to the maximum allowed amount
		// EXCEPT if it's a syn OR we're recovering, then there's always room
		if k.pipelineMap.Len() == pipelineLimit && ksMessage.Type != ksmsg.Syn && !k.recovering {
			k.logger.Debug("Pipeline full, waiting to send next message")
			k.pipelineOpen.Wait()
			k.logger.Debug("Pipeline open, sending message")
		}

		k.pipelineMap.Set(hash, ksMessage)
		return nil
	}
}

func (k *Keysplitting) BuildSyn(action string, payload interface{}, send bool) (*ksmsg.KeysplittingMessage, error) {
	// lock our pipeline because nothing can be calculated until we get our synack
	k.pipelineLock.Lock()
	if k.synAction == "initial" {
		k.synAction = action
	}

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal action params")
	}

	// Build the BZero Certificate then store hash for future messages
	bzCert, err := k.buildBZCert()
	if err != nil {
		return nil, fmt.Errorf("error building bzecert: %s", err)
	} else {
		if hash, ok := bzCert.Hash(); ok {
			k.bzcertHash = hash
		} else {
			return nil, fmt.Errorf("could not hash BZ Certificate")
		}
	}

	// Build the keysplitting message
	synPayload := ksmsg.SynPayload{
		SchemaVersion: ksmsg.SchemaVersion,
		Type:          string(ksmsg.Syn),
		Action:        k.synAction,
		ActionPayload: payloadBytes,
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
		return nil, fmt.Errorf("could not sign payload: %s", err)
	} else if err := k.addToPipelineMap(ksMessage); err != nil {
		return nil, err
	} else {
		if send {
			k.outboxQueue <- &ksMessage
		}
		return &ksMessage, nil
	}
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
		return nil, fmt.Errorf("could not open config file: %s", err)
	} else if configFileBytes, err := ioutil.ReadAll(configFile); err != nil {
		return nil, fmt.Errorf("failed to read config file: %s", err)
	} else if err := json.Unmarshal(configFileBytes, &config); err != nil {
		return nil, fmt.Errorf("could not unmarshal config file: %s", err)
	} else {
		return &config, nil
	}
}
