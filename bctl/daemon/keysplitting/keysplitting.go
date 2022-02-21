package keysplitting

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"time"

	bzcrt "bastionzero.com/bctl/v1/bzerolib/keysplitting/bzcert"
	ksmsg "bastionzero.com/bctl/v1/bzerolib/keysplitting/message"
	"bastionzero.com/bctl/v1/bzerolib/keysplitting/util"
	"github.com/Masterminds/semver"
)

const (
	schemaVersion = "1.0"
)

type Config struct {
	KSConfig KeysplittingConfig `json:"keySplitting"`
	TokenSet TokenSetConfig     `json:"tokenSet"`
}

type KeysplittingConfig struct {
	PrivateKey       string `json:"privateKey"`
	PublicKey        string `json:"publicKey"`
	CerRand          string `json:"cerRand"`
	CerRandSignature string `json:"cerRandSig"`
	InitialIdToken   string `json:"initialIdToken"`
}

type TokenSetConfig struct {
	CurrentIdToken string `json:"id_token"`
}

type BZCertMetadata struct {
	Cert bzcrt.BZCert
	Exp  time.Time
}

type Keysplitting struct {
	hPointer         string
	expectedHPointer string
	publickey        string
	privatekey       string

	// daemon variables
	base64EncodedAgentPubKey string
	daemonVersion            string
	configPath               string
	bzcertHash               string
	refreshTokenCommand      string

	// Protocol differences due to agent version
	checkAgentSignatureConstraint *semver.Constraints
	shouldCheckAgentSignatures    bool
}

func New(
	base64EncodedAgentPubKey string,
	daemonVersion string,
	configPath string,
	refreshTokenCommand string,
) (*Keysplitting, error) {
	// TODO: load keys from storage
	keysplitter := &Keysplitting{
		hPointer:                 "",
		expectedHPointer:         "",
		daemonVersion:            daemonVersion,
		base64EncodedAgentPubKey: base64EncodedAgentPubKey,
		configPath:               configPath,
		refreshTokenCommand:      refreshTokenCommand,
	}

	// Create constraint on validating agent's signatures. All agent versions >=
	// 4.0.2 sign their messages with the expected agent key.
	constraintCheckAgentSig, err := semver.NewConstraint(">= 4.0.2")
	if err != nil {
		return nil, fmt.Errorf("failed to create check agent signature constraint: %w", err)
	}
	keysplitter.checkAgentSignatureConstraint = constraintCheckAgentSig

	return keysplitter, nil
}

func (k *Keysplitting) Validate(ksMessage *ksmsg.KeysplittingMessage) error {
	var hpointer string
	switch ksMessage.Type {
	case ksmsg.SynAck:
		synAckPayload := ksMessage.KeysplittingPayload.(ksmsg.SynAckPayload)

		// Parse agentVersion
		if synAckPayload.AgentVersion != "" {
			v, err := semver.NewVersion(synAckPayload.AgentVersion)
			if err != nil {
				return fmt.Errorf("failed to parse agent version (%v) as semver: %w", synAckPayload.AgentVersion, err)
			}
			k.shouldCheckAgentSignatures = k.checkAgentSignatureConstraint.Check(v)
		}

		hpointer = synAckPayload.HPointer
	case ksmsg.DataAck:
		dataAckPayload := ksMessage.KeysplittingPayload.(ksmsg.DataAckPayload)
		hpointer = dataAckPayload.HPointer
	default:
		return fmt.Errorf("error validating unhandled Keysplitting type")
	}

	// Verify the signature if agent has specific version. This is done for
	// backwards compatability reasons in case an agent hasn't updated.
	if k.shouldCheckAgentSignatures {
		if err := ksMessage.VerifySignature(k.base64EncodedAgentPubKey); err != nil {
			return fmt.Errorf("failed to verify %v signature: %w", ksMessage.Type, err)
		}
	}

	// Verify received hash pointer matches expected hash pointer
	if hpointer != k.expectedHPointer {
		return fmt.Errorf("%T hash pointer did not match expected hash pointer", ksMessage.KeysplittingPayload)
	}

	return nil
}

func (k *Keysplitting) BuildResponse(ksMessage *ksmsg.KeysplittingMessage, action string, actionPayload []byte) (ksmsg.KeysplittingMessage, error) {
	var responseMessage ksmsg.KeysplittingMessage

	switch ksMessage.Type {
	case ksmsg.SynAck:
		synAckPayload := ksMessage.KeysplittingPayload.(ksmsg.SynAckPayload)
		if dataPayload, hash, err := synAckPayload.BuildResponsePayload(action, actionPayload, k.bzcertHash); err != nil {
			return ksmsg.KeysplittingMessage{}, err
		} else {
			k.hPointer = hash
			responseMessage = ksmsg.KeysplittingMessage{
				Type:                ksmsg.Data,
				KeysplittingPayload: dataPayload,
			}
		}
	case ksmsg.DataAck:
		dataAckPayload := ksMessage.KeysplittingPayload.(ksmsg.DataAckPayload)
		if dataPayload, hash, err := dataAckPayload.BuildResponsePayload(action, actionPayload, k.bzcertHash); err != nil {
			return ksmsg.KeysplittingMessage{}, err
		} else {
			k.hPointer = hash
			responseMessage = ksmsg.KeysplittingMessage{
				Type:                ksmsg.Data,
				KeysplittingPayload: dataPayload,
			}
		}
	}

	hashBytes, _ := util.HashPayload(responseMessage.KeysplittingPayload)
	k.expectedHPointer = base64.StdEncoding.EncodeToString(hashBytes)

	if err := responseMessage.Sign(k.privatekey); err != nil {
		return responseMessage, fmt.Errorf("could not sign payload: %v", err.Error())
	} else {
		return responseMessage, nil
	}
}

func (k *Keysplitting) BuildSyn(action string, payload []byte) (ksmsg.KeysplittingMessage, error) {
	// If this is the beginning of the hash chain, then we create a nonce with a random value,
	// otherwise we use the hash of the previous value to maintain the hash chain and immutability
	var nonce string
	if k.expectedHPointer == "" {
		nonce = util.Nonce()
	} else {
		nonce = k.expectedHPointer
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
		SchemaVersion: schemaVersion,
		Type:          string(ksmsg.Syn),
		Action:        action,
		ActionPayload: payload,
		DaemonVersion: k.daemonVersion,
		TargetId:      k.base64EncodedAgentPubKey,
		Nonce:         nonce,
		BZCert:        bzCert,
	}

	ksMessage := ksmsg.KeysplittingMessage{
		Type:                ksmsg.Syn,
		KeysplittingPayload: synPayload,
	}

	// Sign it and send it
	if err := ksMessage.Sign(k.privatekey); err != nil {
		return ksMessage, fmt.Errorf("could not sign payload: %v", err.Error())
	} else {
		hashBytes, _ := util.HashPayload(synPayload)
		k.expectedHPointer = base64.StdEncoding.EncodeToString(hashBytes)
		return ksMessage, nil
	}
}

func (k *Keysplitting) buildBZCert() (bzcrt.BZCert, error) {
	// update the id token by calling the passed in zli command
	if err := util.RunRefreshAuthCommand(k.refreshTokenCommand); err != nil {
		return bzcrt.BZCert{}, err
	}

	if configFile, err := os.Open(k.configPath); err != nil {
		return bzcrt.BZCert{}, fmt.Errorf("could not open config file: %v", err.Error())
	} else {
		configFileBytes, _ := ioutil.ReadAll(configFile)

		var config Config
		err := json.Unmarshal(configFileBytes, &config)
		if err != nil {
			return bzcrt.BZCert{}, fmt.Errorf("could not unmarshal config file")
		}

		// Set public and private keys because someone maybe have logged out and logged back in again
		k.publickey = config.KSConfig.PublicKey

		// The golang ed25519 library uses a length 64 private key because the private key is the concatenated form
		// privatekey = privatekey + publickey.  So if it was generated as length 32, we can correct for that here
		if privatekeyBytes, _ := base64.StdEncoding.DecodeString(config.KSConfig.PrivateKey); len(privatekeyBytes) == 32 {
			publickeyBytes, _ := base64.StdEncoding.DecodeString(k.publickey)
			k.privatekey = base64.StdEncoding.EncodeToString(append(privatekeyBytes, publickeyBytes...))
		} else {
			k.privatekey = config.KSConfig.PrivateKey
		}

		return bzcrt.BZCert{
			InitialIdToken:  config.KSConfig.InitialIdToken,
			CurrentIdToken:  config.TokenSet.CurrentIdToken,
			ClientPublicKey: config.KSConfig.PublicKey,
			Rand:            config.KSConfig.CerRand,
			SignatureOnRand: config.KSConfig.CerRandSignature,
		}, nil
	}
}
