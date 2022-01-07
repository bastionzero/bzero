package mrzap

import (
	ed "crypto/ed25519"
	"encoding/base64"
	"fmt"
	"time"

	"bastionzero.com/bctl/v1/bctl/agent/vault"
	bzcrt "bastionzero.com/bctl/v1/bzerolib/mrzap/bzcert"
	mzmsg "bastionzero.com/bctl/v1/bzerolib/mrzap/message"
	"bastionzero.com/bctl/v1/bzerolib/mrzap/util"
)

type BZCertMetadata struct {
	Cert bzcrt.BZCert
	Exp  time.Time
}

type IMrZAP interface {
	GetHpointer() string
	Validate(mzMessage *mzmsg.MrZAPMessage) error
	BuildResponse(mzMessage *mzmsg.MrZAPMessage, action string, actionPayload []byte) (mzmsg.MrZAPMessage, error)
}

type MrZAP struct {
	hPointer         string
	expectedHPointer string
	bzCerts          map[string]BZCertMetadata // only for agent
	publickey        string
	privatekey       string
	idpProvider      string
	idpOrgId         string
	orgId            string
}

func New() (IMrZAP, error) {
	// Generate public private key pair along ed25519 curve
	if publicKey, privateKey, err := ed.GenerateKey(nil); err != nil {
		return &MrZAP{}, fmt.Errorf("error generating key pair: %v", err.Error())
	} else {
		pubkeyString := base64.StdEncoding.EncodeToString([]byte(publicKey))
		privkeyString := base64.StdEncoding.EncodeToString([]byte(privateKey))

		// Load in our idp infomation from the vault as well
		config, _ := vault.LoadVault()

		return &MrZAP{
			bzCerts:     make(map[string]BZCertMetadata),
			publickey:   pubkeyString,
			privatekey:  privkeyString,
			idpProvider: config.Data.IdpProvider,
			idpOrgId:    config.Data.IdpOrgId,
			orgId:       config.Data.OrgId,
		}, nil
	}
}

func (k *MrZAP) GetHpointer() string {
	return k.hPointer
}

func (k *MrZAP) Validate(mzMessage *mzmsg.MrZAPMessage) error {
	switch mzMessage.Type {
	case mzmsg.Syn:
		synPayload := mzMessage.MrZAPPayload.(mzmsg.SynPayload)

		// Verify the BZCert
		if hash, exp, err := synPayload.BZCert.Verify(k.idpProvider, k.idpOrgId); err != nil {
			return err
		} else {
			k.bzCerts[hash] = BZCertMetadata{
				Cert: synPayload.BZCert,
				Exp:  exp,
			}
		}

		// Verify the Signature
		if err := mzMessage.VerifySignature(synPayload.BZCert.ClientPublicKey); err != nil {
			return err
		}

		// Make sure targetId matches
		// if synPayload.TargetId != k.publickey {
		// 	return fmt.Errorf("syn's TargetId did not match Target's actual ID")
		// }
	case mzmsg.Data:
		dataPayload := mzMessage.MrZAPPayload.(mzmsg.DataPayload)

		// Check BZCert matches one we have stored
		if certMetadata, ok := k.bzCerts[dataPayload.BZCertHash]; !ok {
			return fmt.Errorf("could not match BZCert hash to one previously received")
		} else {

			// Verify the Signature
			if err := mzMessage.VerifySignature(certMetadata.Cert.ClientPublicKey); err != nil {
				return err
			}
		}

		// Verify received hash pointer matches expected
		if dataPayload.HPointer != k.expectedHPointer {
			return fmt.Errorf("data's hash pointer did not match expected")
		}

		// Make sure targetId matches
		// if dataPayload.TargetId != k.publickey {
		// 	return fmt.Errorf("data's TargetId did not match Target's actual ID")
		// }
	default:
		return fmt.Errorf("error validating unhandled MrZAP type")
	}
	return nil
}

func (k *MrZAP) BuildResponse(mzMessage *mzmsg.MrZAPMessage, action string, actionPayload []byte) (mzmsg.MrZAPMessage, error) {
	var responseMessage mzmsg.MrZAPMessage

	switch mzMessage.Type {
	case mzmsg.Syn:
		synPayload := mzMessage.MrZAPPayload.(mzmsg.SynPayload)
		if synAckPayload, hash, err := synPayload.BuildResponsePayload(actionPayload, k.publickey); err != nil {
			return mzmsg.MrZAPMessage{}, err
		} else {
			k.hPointer = hash
			responseMessage = mzmsg.MrZAPMessage{
				Type:         mzmsg.SynAck,
				MrZAPPayload: synAckPayload,
			}
		}
	case mzmsg.Data:
		dataPayload := mzMessage.MrZAPPayload.(mzmsg.DataPayload)
		if dataAckPayload, hash, err := dataPayload.BuildResponsePayload(actionPayload, k.publickey); err != nil {
			return mzmsg.MrZAPMessage{}, err
		} else {
			k.hPointer = hash
			responseMessage = mzmsg.MrZAPMessage{
				Type:         mzmsg.DataAck,
				MrZAPPayload: dataAckPayload,
			}
		}
	}

	hashBytes, _ := util.HashPayload(responseMessage.MrZAPPayload)
	k.expectedHPointer = base64.StdEncoding.EncodeToString(hashBytes)

	// Sign it and send it
	if err := responseMessage.Sign(k.privatekey); err != nil {
		return responseMessage, fmt.Errorf("could not sign payload: %v", err.Error())
	} else {
		return responseMessage, nil
	}
}
