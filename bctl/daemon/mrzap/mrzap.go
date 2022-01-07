package mrzap

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"time"

	bzcrt "bastionzero.com/bctl/v1/bzerolib/mrzap/bzcert"
	mzmsg "bastionzero.com/bctl/v1/bzerolib/mrzap/message"
	"bastionzero.com/bctl/v1/bzerolib/mrzap/util"
)

const (
	schemaVersion = "1.0"
)

type Config struct {
	MZConfig MrZAPConfig    `json:"mrZAP"`
	TokenSet TokenSetConfig `json:"tokenSet"`
}

type MrZAPConfig struct {
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

type IMrZAP interface {
	BuildSyn(action string, payload []byte) (mzmsg.MrZAPMessage, error)
	Validate(mzMessage *mzmsg.MrZAPMessage) error
	BuildResponse(mzMessage *mzmsg.MrZAPMessage, action string, actionPayload []byte) (mzmsg.MrZAPMessage, error)
}

type MrZAP struct {
	hPointer         string
	expectedHPointer string
	publickey        string
	privatekey       string

	// daemon variables
	targetId            string
	configPath          string
	bzcertHash          string
	refreshTokenCommand string
}

func New(targetId string, configPath string, refreshTokenCommand string) (IMrZAP, error) {

	// TODO: load keys from storage
	zapper := &MrZAP{
		hPointer:            "",
		expectedHPointer:    "",
		targetId:            targetId,
		configPath:          configPath,
		refreshTokenCommand: refreshTokenCommand,
	}

	return zapper, nil
}

func (m *MrZAP) Validate(mzMessage *mzmsg.MrZAPMessage) error {
	var hpointer string
	switch mzMessage.Type {
	case mzmsg.SynAck:
		synAckPayload := mzMessage.MrZAPPayload.(mzmsg.SynAckPayload)
		hpointer = synAckPayload.HPointer
	case mzmsg.DataAck:
		dataAckPayload := mzMessage.MrZAPPayload.(mzmsg.DataAckPayload)
		hpointer = dataAckPayload.HPointer
	default:
		return fmt.Errorf("error validating unhandled MrZAP type")
	}

	// Verify received hash pointer matches expected
	if hpointer != m.expectedHPointer {
		return fmt.Errorf("%T hash pointer did not match expected", mzMessage.MrZAPPayload)
	} else {
		return nil
	}
}

func (m *MrZAP) BuildResponse(mzMessage *mzmsg.MrZAPMessage, action string, actionPayload []byte) (mzmsg.MrZAPMessage, error) {
	var responseMessage mzmsg.MrZAPMessage

	switch mzMessage.Type {
	case mzmsg.SynAck:
		synAckPayload := mzMessage.MrZAPPayload.(mzmsg.SynAckPayload)
		if dataPayload, hash, err := synAckPayload.BuildResponsePayload(action, actionPayload, m.bzcertHash); err != nil {
			return mzmsg.MrZAPMessage{}, err
		} else {
			m.hPointer = hash
			responseMessage = mzmsg.MrZAPMessage{
				Type:         mzmsg.Data,
				MrZAPPayload: dataPayload,
			}
		}
	case mzmsg.DataAck:
		dataAckPayload := mzMessage.MrZAPPayload.(mzmsg.DataAckPayload)
		if dataPayload, hash, err := dataAckPayload.BuildResponsePayload(action, actionPayload, m.bzcertHash); err != nil {
			return mzmsg.MrZAPMessage{}, err
		} else {
			m.hPointer = hash
			responseMessage = mzmsg.MrZAPMessage{
				Type:         mzmsg.Data,
				MrZAPPayload: dataPayload,
			}
		}
	}

	hashBytes, _ := util.HashPayload(responseMessage.MrZAPPayload)
	m.expectedHPointer = base64.StdEncoding.EncodeToString(hashBytes)

	if err := responseMessage.Sign(m.privatekey); err != nil {
		return responseMessage, fmt.Errorf("could not sign payload: %v", err.Error())
	} else {
		return responseMessage, nil
	}
}

func (m *MrZAP) BuildSyn(action string, payload []byte) (mzmsg.MrZAPMessage, error) {
	// If this is the beginning of the hash chain, then we create a nonce with a random value,
	// otherwise we use the hash of the previous value to maintain the hash chain and immutability
	var nonce string
	if m.expectedHPointer == "" {
		nonce = util.Nonce()
	} else {
		nonce = m.expectedHPointer
	}

	// Build the BZero Certificate then store hash for future messages
	bzCert, err := m.buildBZCert()
	if err != nil {
		return mzmsg.MrZAPMessage{}, fmt.Errorf("error building bzecert: %v", err.Error())
	} else {
		if hash, ok := bzCert.Hash(); ok {
			m.bzcertHash = hash
		} else {
			return mzmsg.MrZAPMessage{}, fmt.Errorf("could not hash BZ Certificate")
		}
	}

	// Build the mrzap message
	synPayload := mzmsg.SynPayload{
		Timestamp:     fmt.Sprint(time.Now().Unix()),
		SchemaVersion: schemaVersion,
		Type:          string(mzmsg.Syn),
		Action:        action,
		ActionPayload: payload,
		TargetId:      m.targetId, // TODO
		Nonce:         nonce,
		BZCert:        bzCert,
	}

	mzMessage := mzmsg.MrZAPMessage{
		Type:         mzmsg.Syn,
		MrZAPPayload: synPayload,
	}

	// Sign it and send it
	if err := mzMessage.Sign(m.privatekey); err != nil {
		return mzMessage, fmt.Errorf("could not sign payload: %v", err.Error())
	} else {
		hashBytes, _ := util.HashPayload(synPayload)
		m.expectedHPointer = base64.StdEncoding.EncodeToString(hashBytes)
		return mzMessage, nil
	}
}

func (m *MrZAP) buildBZCert() (bzcrt.BZCert, error) {
	// update the id token by calling the passed in zli command
	if err := util.RunRefreshAuthCommand(m.refreshTokenCommand); err != nil {
		return bzcrt.BZCert{}, err
	}

	if configFile, err := os.Open(m.configPath); err != nil {
		return bzcrt.BZCert{}, fmt.Errorf("could not open config file: %v", err.Error())
	} else {
		configFileBytes, _ := ioutil.ReadAll(configFile)

		var config Config
		err := json.Unmarshal(configFileBytes, &config)
		if err != nil {
			return bzcrt.BZCert{}, fmt.Errorf("could not unmarshal config file")
		}

		// Set public and private keys because someone maybe have logged out and logged back in again
		m.publickey = config.MZConfig.PublicKey

		// The golang ed25519 library uses a length 64 private key because the private key is the concatenated form
		// privatekey = privatekey + publickey.  So if it was generated as length 32, we can correct for that here
		if privatekeyBytes, _ := base64.StdEncoding.DecodeString(config.MZConfig.PrivateKey); len(privatekeyBytes) == 32 {
			publickeyBytes, _ := base64.StdEncoding.DecodeString(m.publickey)
			m.privatekey = base64.StdEncoding.EncodeToString(append(privatekeyBytes, publickeyBytes...))
		} else {
			m.privatekey = config.MZConfig.PrivateKey
		}

		return bzcrt.BZCert{
			InitialIdToken:  config.MZConfig.InitialIdToken,
			CurrentIdToken:  config.TokenSet.CurrentIdToken,
			ClientPublicKey: config.MZConfig.PublicKey,
			Rand:            config.MZConfig.CerRand,
			SignatureOnRand: config.MZConfig.CerRandSignature,
		}, nil
	}
}
