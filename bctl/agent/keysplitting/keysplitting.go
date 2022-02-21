package keysplitting

import (
	"encoding/base64"
	"fmt"
	"time"

	bzcrt "bastionzero.com/bctl/v1/bzerolib/keysplitting/bzcert"
	ksmsg "bastionzero.com/bctl/v1/bzerolib/keysplitting/message"
	"bastionzero.com/bctl/v1/bzerolib/keysplitting/util"
)

type BZCertMetadata struct {
	Cert bzcrt.BZCert
	Exp  time.Time
}

type Keysplitting struct {
	hPointer         string
	expectedHPointer string
	bzCerts          map[string]BZCertMetadata // only for agent
	publickey        string
	privatekey       string
	idpProvider      string
	idpOrgId         string
}

func New(
	base64EncodedPublicKey string,
	base64EncodedPrivateKey string,
	idpProvider string,
	idpOrgId string) *Keysplitting {

	return &Keysplitting{
		hPointer:         "",
		expectedHPointer: "",
		bzCerts:          make(map[string]BZCertMetadata),
		publickey:        base64EncodedPublicKey,
		privatekey:       base64EncodedPrivateKey,
		idpProvider:      idpProvider,
		idpOrgId:         idpOrgId,
	}
}

func (k *Keysplitting) GetHpointer() string {
	return k.hPointer
}

func (k *Keysplitting) Validate(ksMessage *ksmsg.KeysplittingMessage) error {
	switch ksMessage.Type {
	case ksmsg.Syn:
		synPayload := ksMessage.KeysplittingPayload.(ksmsg.SynPayload)

		// Verify the BZCert
		hash, exp, err := synPayload.BZCert.Verify(k.idpProvider, k.idpOrgId)
		if err != nil {
			return fmt.Errorf("failed to verify SYN's BZCert: %w", err)
		}

		// Verify the signature
		if err := ksMessage.VerifySignature(synPayload.BZCert.ClientPublicKey); err != nil {
			return fmt.Errorf("failed to verify SYN's signature: %w", err)
		}

		// Verify the SYN message's targetId field only if the message also
		// contains daemonVersion. Daemons that do not set daemonVersion also do
		// not set targetId. This is done for backwards compatability reasons in
		// case a daemon hasn't updated.
		if synPayload.DaemonVersion != "" {
			// Verify SYN message commits to this agent's cryptographic identity
			if synPayload.TargetId != k.publickey {
				return fmt.Errorf("SYN's TargetId did not match agent's public key")
			}
		}

		// All checks have passed. Add cert to dict of known bzCerts
		k.bzCerts[hash] = BZCertMetadata{
			Cert: synPayload.BZCert,
			Exp:  exp,
		}
	case ksmsg.Data:
		dataPayload := ksMessage.KeysplittingPayload.(ksmsg.DataPayload)

		// Check BZCert matches one we have stored
		certMetadata, ok := k.bzCerts[dataPayload.BZCertHash]
		if !ok {
			return fmt.Errorf("could not match DATA's BZCert hash to one previously received")
		}

		// Verify the signature
		if err := ksMessage.VerifySignature(certMetadata.Cert.ClientPublicKey); err != nil {
			return err
		}

		// Check that BZCert isn't expired
		if time.Now().After(certMetadata.Exp) {
			return fmt.Errorf("DATA's referenced BZCert has expired")
		}

		// Verify received hash pointer matches expected
		if dataPayload.HPointer != k.expectedHPointer {
			return fmt.Errorf("DATA's hash pointer did not match expected hash pointer")
		}
	default:
		return fmt.Errorf("error validating unhandled Keysplitting type")
	}
	return nil
}

func (k *Keysplitting) BuildResponse(ksMessage *ksmsg.KeysplittingMessage, action string, actionPayload []byte) (ksmsg.KeysplittingMessage, error) {
	var responseMessage ksmsg.KeysplittingMessage

	switch ksMessage.Type {
	case ksmsg.Syn:
		synPayload := ksMessage.KeysplittingPayload.(ksmsg.SynPayload)
		if synAckPayload, hash, err := synPayload.BuildResponsePayload(actionPayload, k.publickey); err != nil {
			return ksmsg.KeysplittingMessage{}, err
		} else {
			k.hPointer = hash
			responseMessage = ksmsg.KeysplittingMessage{
				Type:                ksmsg.SynAck,
				KeysplittingPayload: synAckPayload,
			}
		}
	case ksmsg.Data:
		dataPayload := ksMessage.KeysplittingPayload.(ksmsg.DataPayload)
		if dataAckPayload, hash, err := dataPayload.BuildResponsePayload(actionPayload, k.publickey); err != nil {
			return ksmsg.KeysplittingMessage{}, err
		} else {
			k.hPointer = hash
			responseMessage = ksmsg.KeysplittingMessage{
				Type:                ksmsg.DataAck,
				KeysplittingPayload: dataAckPayload,
			}
		}
	}

	hashBytes, _ := util.HashPayload(responseMessage.KeysplittingPayload)
	k.expectedHPointer = base64.StdEncoding.EncodeToString(hashBytes)

	// Sign it and send it
	if err := responseMessage.Sign(k.privatekey); err != nil {
		return responseMessage, fmt.Errorf("could not sign payload: %v", err.Error())
	} else {
		return responseMessage, nil
	}
}
