package keysplitting

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/Masterminds/semver"
	orderedmap "github.com/wk8/go-ordered-map"

	"bastionzero.com/bctl/v1/bctl/daemon/keysplitting/tokenrefresh"
	rrr "bastionzero.com/bctl/v1/bzerolib/error"
	bzcrt "bastionzero.com/bctl/v1/bzerolib/keysplitting/bzcert"
	ksmsg "bastionzero.com/bctl/v1/bzerolib/keysplitting/message"
	"bastionzero.com/bctl/v1/bzerolib/keysplitting/util"
	"bastionzero.com/bctl/v1/bzerolib/logger"
)

// Max number of times we will try to resend after an error message
const MaxErrorRecoveryTries = 3

// The number of messages we're allowed to precalculate and send without having
// received an ack
const PipelineLimit = 8

type TokenRefresher interface {
	Refresh() (*tokenrefresh.ZLIKeysplittingConfig, error)
}

type Keysplitting struct {
	logger *logger.Logger

	clientPubKey    string
	clientSecretKey string
	bzcertHash      string

	agentPubKey  string
	ackPublicKey string

	// for grabbing and updating id tokens
	tokenRefresher TokenRefresher

	// pipelineLock mutex coordinates usage of pipelineMap, isHandshakeComplete,
	// and lastAck
	pipelineLock sync.Mutex
	pipelineOpen *sync.Cond
	// ordered hash map to keep track of sent keysplitting messages
	pipelineMap *orderedmap.OrderedMap
	// isHandshakeComplete is true when SynAck has been received. It is reset to
	// false during recovery
	isHandshakeComplete bool
	// not the last ack we've received but the last ack we've received *in order*
	lastAck *ksmsg.KeysplittingMessage

	// a channel for all the messages we give the datachannel to send
	outboxQueue chan *ksmsg.KeysplittingMessage

	// bool variable for letting the datachannel know when to start processing
	// incoming messages again
	recovering bool

	// We set the schemaVersion to use based on the schemaVersion sent by the agent in the synack
	schemaVersion      *semver.Version
	prePipeliningAgent bool
	synAction          string

	// keep track of how many times we've tried to recover
	errorRecoveryAttempt int

	pipelineLimit int
}

func New(
	logger *logger.Logger,
	agentPubKey string,
	tokenRefresher TokenRefresher,
) (*Keysplitting, error) {

	// TODO: load keys from storage
	keysplitter := &Keysplitting{
		logger:               logger,
		agentPubKey:          agentPubKey,
		tokenRefresher:       tokenRefresher,
		ackPublicKey:         "",
		pipelineMap:          orderedmap.New(),
		outboxQueue:          make(chan *ksmsg.KeysplittingMessage, PipelineLimit),
		recovering:           false,
		synAction:            "initial",
		errorRecoveryAttempt: 0,
		isHandshakeComplete:  false,
		lastAck:              nil,
	}
	keysplitter.pipelineOpen = sync.NewCond(&keysplitter.pipelineLock)
	// Default to global constant
	keysplitter.pipelineLimit = PipelineLimit

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
		} else if pair := k.pipelineMap.GetPair(errMessage.HPointer); pair == nil && !k.recovering {
			k.logger.Infof("agent error is not on a message sent by this datachannel")
			return nil // not a fatal error
		} else if msg, ok := pair.Value.(ksmsg.KeysplittingMessage); ok && msg.Type == ksmsg.Syn {
			return fmt.Errorf("unable to recover because we hit an error on our syn message: %s", errMessage.Message)
		} else if k.recovering {
			k.logger.Infof("ignoring error message because we're already in recovery")
			return nil // not a fatal error
		}
	}

	if k.errorRecoveryAttempt >= MaxErrorRecoveryTries {
		return fmt.Errorf("retried too many times to fix error: %s", errMessage.Message)
	} else {
		k.errorRecoveryAttempt++
		k.logger.Infof("Attempt #%d to recover from error: %s", k.errorRecoveryAttempt, errMessage.Message)
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
			return fmt.Errorf("%w: failed to verify %v signature: inner error: %s outer error: %s", ErrInvalidSignature, ksMessage.Type, innerErr, err)
		}
	}

	hpointer, err := ksMessage.GetHpointer()
	if err != nil {
		return err
	}

	// Lock pipelineMap's mutex before accessing it
	k.pipelineLock.Lock()
	defer k.pipelineLock.Unlock()

	// Check this messages is in response to one we've sent
	if _, ok := k.pipelineMap.Get(hpointer); ok {
		switch ksMessage.Type {
		case ksmsg.SynAck:
			if msg, ok := ksMessage.KeysplittingPayload.(ksmsg.SynAckPayload); ok {
				k.lastAck = ksMessage
				k.pipelineMap.Delete(hpointer) // delete syn from map

				// when we recover, we're recovering based on the nonce in the syn/ack because unless
				// it's not in response to the initial syn, where the nonce is a true random number,
				// it is an hpointer which refers to the agent's last recieved and validated message.
				// aka it is the current state of the mrzap hash chain according to the agent and this
				// recovery mechanism allows us to sync our mrzap state to that
				k.recovering = false
				k.resend(msg.Nonce)

				parsedSchemaVersion, err := semver.NewVersion(msg.SchemaVersion)
				if err != nil {
					return ErrFailedToParseVersion
				}
				k.schemaVersion = parsedSchemaVersion

				// check to see if we're talking with an agent that's using
				// pre-2.0 keysplitting because we'll need to dirty the payload
				// by adding extra quotes around it TODO: CWC-1820: remove once
				// all daemon's are updated
				if c, err := semver.NewConstraint("< 2.0"); err != nil {
					return fmt.Errorf("unable to create versioning constraint")
				} else {
					k.prePipeliningAgent = c.Check(parsedSchemaVersion)

					if k.prePipeliningAgent {
						// Override default
						k.pipelineLimit = 1
					}
				}

				// We've received a SynAck, so the handshake is complete
				k.isHandshakeComplete = true
			}
		case ksmsg.DataAck:
			k.lastAck = ksMessage
			k.pipelineMap.Delete(hpointer)

			// If we're here, it means that the previous data message that
			// caused the error was accepted
			k.errorRecoveryAttempt = 0
		}

		// Condition variable changed. We must call Broadcast() to prevent
		// deadlock
		k.pipelineOpen.Broadcast()
	} else {
		return fmt.Errorf("%w: %T message did not correspond to a previously sent message", ErrUnknownHPointer, ksMessage.KeysplittingPayload)
	}

	return nil
}

func (k *Keysplitting) Inbox(action string, actionPayload []byte) error {
	k.pipelineLock.Lock()
	defer k.pipelineLock.Unlock()

	// Wait if pipeline is full OR if handshake is not complete
	for k.pipelineMap.Len() >= k.pipelineLimit || !k.isHandshakeComplete {
		k.logger.Debug(fmt.Sprintf("Pipeline length: %v, Pipeline full: %v, Handshake complete: %v. Waiting to send next message...", k.pipelineMap.Len(), k.pipelineMap.Len() >= k.pipelineLimit, k.isHandshakeComplete))
		k.pipelineOpen.Wait()
	}

	return k.pipeline(action, actionPayload)
}

func (k *Keysplitting) pipeline(action string, actionPayload []byte) error {
	if action == "" {
		return fmt.Errorf("i'm not allowed to build a keysplitting message with empty action")
	}

	// get the ack we're going to be building our new message off of
	var ack *ksmsg.KeysplittingMessage
	if pair := k.pipelineMap.Newest(); pair == nil {
		// if our pipeline map is empty, we build off our last received ack.
		// lastAck is guaranteed to be set because pipeline() is only called
		// after handshake is complete
		ack = k.lastAck
	} else {
		// otherwise, we're going to need to predict the ack we're building off of
		ksMessage := pair.Value.(ksmsg.KeysplittingMessage)
		if newAck, err := ksMessage.BuildUnsignedDataAck([]byte{}, k.agentPubKey, k.schemaVersion.Original()); err != nil {
			return fmt.Errorf("failed to predict ack: %s", err)
		} else {
			ack = &newAck
		}
	}

	// build our new data message and then ship it!
	if newMessage, err := k.buildResponse(ack, action, actionPayload); err != nil {
		return fmt.Errorf("failed to build new message: %w", err)
	} else if err := k.addToPipelineMap(newMessage); err != nil {
		return err
	} else {
		k.outboxQueue <- &newMessage
		return nil
	}
}

func (k *Keysplitting) buildResponse(ksMessage *ksmsg.KeysplittingMessage, action string, payload []byte) (ksmsg.KeysplittingMessage, error) {
	// TODO: CWC-1820: remove this if statement once all daemon's are updated
	if k.prePipeliningAgent {
		// if we're talking with an old agent, then we have to add extra quotes

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

	// Use the agreed upon schema version from the synack when building data messages
	if responseMessage, err := ksMessage.BuildUnsignedData(action, payload, k.bzcertHash, k.schemaVersion.Original()); err != nil {
		return responseMessage, err
	} else if err := responseMessage.Sign(k.clientSecretKey); err != nil {
		return responseMessage, fmt.Errorf("%w: %s", ErrFailedToSign, err)
	} else {
		return responseMessage, nil
	}
}

func (k *Keysplitting) addToPipelineMap(ksMessage ksmsg.KeysplittingMessage) error {
	if hash := ksMessage.Hash(); hash == "" {
		return fmt.Errorf("failed to hash message")
	} else {
		k.pipelineMap.Set(hash, ksMessage)
		return nil
	}
}

func (k *Keysplitting) BuildSyn(action string, payload interface{}, send bool) (*ksmsg.KeysplittingMessage, error) {
	// lock our pipeline mutex because we are accessing pipelineMap and
	// isHandshakeComplete
	k.pipelineLock.Lock()
	defer k.pipelineLock.Unlock()

	// Reset state
	k.isHandshakeComplete = false
	k.lastAck = nil

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
		return nil, fmt.Errorf("error building bzecert: %w", err)
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
		return nil, fmt.Errorf("%w: %s", ErrFailedToSign, err)
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

	zliConfig, err := k.tokenRefresher.Refresh()
	if err != nil {
		return bzcrt.BZCert{}, fmt.Errorf("failed to refresh keysplitting token: %w", err)
	}

	// Set public and private keys because someone maybe have logged out and
	// logged back in again
	k.clientPubKey = zliConfig.KSConfig.PublicKey

	// The golang ed25519 library uses a length 64 private key because the
	// private key is the concatenated form privatekey = privatekey + publickey.
	// So if it was generated as length 32, we can correct for that here
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
