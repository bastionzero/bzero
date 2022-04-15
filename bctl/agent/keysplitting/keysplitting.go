package keysplitting

import (
	"encoding/base64"
	"fmt"
	"time"

	bzcrt "bastionzero.com/bctl/v1/bzerolib/keysplitting/bzcert"
	ksmsg "bastionzero.com/bctl/v1/bzerolib/keysplitting/message"
	"bastionzero.com/bctl/v1/bzerolib/keysplitting/util"
	"github.com/Masterminds/semver"
)

// schema version <= this value doesn't set targetId to the agent's pubkey
const schemaVersionTargetIdNotSet string = "1.0"

type BZCertMetadata struct {
	Hash       string
	Cert       bzcrt.BZCert
	Expiration time.Time
}

type Keysplitting struct {
	hPointer         string
	expectedHPointer string
	bzCert           BZCertMetadata // only for one client
	publickey        string
	privatekey       string
	idpProvider      string
	idpOrgId         string

	// define constraints based on schema version
	shouldCheckTargetId *semver.Constraints
}

type IKeysplittingConfig interface {
	GetPublicKey() string
	GetPrivateKey() string
	GetIdpProvider() string
	GetIdpOrgId() string
}

func New(config IKeysplittingConfig) (*Keysplitting, error) {
	shouldCheckTargetIdConstraint, err := semver.NewConstraint(fmt.Sprintf("> %v", schemaVersionTargetIdNotSet))
	if err != nil {
		return nil, fmt.Errorf("failed to create check target id constraint: %w", err)
	}

	return &Keysplitting{
		hPointer:            "",
		expectedHPointer:    "",
		publickey:           config.GetPublicKey(),
		privatekey:          config.GetPrivateKey(),
		idpProvider:         config.GetIdpProvider(),
		idpOrgId:            config.GetIdpOrgId(),
		shouldCheckTargetId: shouldCheckTargetIdConstraint,
	}, nil
}

func (k *Keysplitting) GetHpointer() string {
	// hpointer is only used when building errors
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

		// Extract semver version to determine if different protocol checks must
		// be done
		v, err := semver.NewVersion(synPayload.SchemaVersion)
		if err != nil {
			return fmt.Errorf("failed to parse schema version (%v) as semver: %w", synPayload.SchemaVersion, err)
		}

		// Daemons with schema version <= 1.0 do not set targetId, so we cannot
		// apply this check universally
		// TODO: CWC-1553: Always check TargetId once all daemons have updated
		if k.shouldCheckTargetId.Check(v) {
			// Verify SYN message commits to this agent's cryptographic identity
			if synPayload.TargetId != k.publickey {
				return fmt.Errorf("SYN's TargetId did not match agent's public key")
			}
		}

		// All checks have passed. Make this BZCert that of the active user
		k.bzCert = BZCertMetadata{
			Hash:       hash,
			Cert:       synPayload.BZCert,
			Expiration: exp,
		}
	case ksmsg.Data:
		dataPayload := ksMessage.KeysplittingPayload.(ksmsg.DataPayload)

		// Check BZCert matches one we have stored
		if k.bzCert.Hash != dataPayload.BZCertHash {
			return fmt.Errorf("DATA's BZCert does not match the active user's")
		}

		// Verify the signature
		if err := ksMessage.VerifySignature(k.bzCert.Cert.ClientPublicKey); err != nil {
			return err
		}

		// Check that BZCert isn't expired
		if time.Now().After(k.bzCert.Expiration) {
			return fmt.Errorf("DATA's referenced BZCert has expired")
		}

		// Verify received hash pointer matches expected
		if dataPayload.HPointer != k.expectedHPointer {
			return fmt.Errorf("DATA's hash pointer did not match expected hash pointer")
		}
	default:
		return fmt.Errorf("error validating unhandled Keysplitting type")
	}

	k.hPointer = ksMessage.Hash()
	return nil
}

func (k *Keysplitting) BuildResponse(ksMessage *ksmsg.KeysplittingMessage, action string, actionPayload []byte) (ksmsg.KeysplittingMessage, error) {
	if responseMessage, _, err := ksMessage.BuildUnsignedAck(actionPayload, k.publickey); err != nil {
		return responseMessage, err
	} else if err := responseMessage.Sign(k.privatekey); err != nil {
		return responseMessage, fmt.Errorf("could not sign payload: %s", err)
	} else if hashBytes, ok := util.HashPayload(responseMessage.KeysplittingPayload); !ok {
		return responseMessage, fmt.Errorf("could not hash payload")
	} else {
		k.expectedHPointer = base64.StdEncoding.EncodeToString(hashBytes)
		return responseMessage, nil
	}
}
