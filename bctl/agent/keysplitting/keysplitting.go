package keysplitting

import (
	"fmt"
	"time"

	bzcrt "bastionzero.com/bctl/v1/bzerolib/keysplitting/bzcert"
	"bastionzero.com/bctl/v1/bzerolib/keysplitting/message"
	ksmsg "bastionzero.com/bctl/v1/bzerolib/keysplitting/message"
	"bastionzero.com/bctl/v1/bzerolib/keysplitting/util"
	"bastionzero.com/bctl/v1/bzerolib/logger"
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
	logger           *logger.Logger
	lastDataMessage  *ksmsg.KeysplittingMessage
	expectedHPointer string
	bzCert           BZCertMetadata // only for one client
	publickey        string
	privatekey       string
	idpProvider      string
	idpOrgId         string

	// define constraints based on schema version
	shouldCheckTargetId *semver.Constraints

	daemonSchemaVersion *semver.Version
}

type IKeysplittingConfig interface {
	GetPublicKey() string
	GetPrivateKey() string
	GetIdpProvider() string
	GetIdpOrgId() string
}

func New(logger *logger.Logger, config IKeysplittingConfig) (*Keysplitting, error) {
	shouldCheckTargetIdConstraint, err := semver.NewConstraint(fmt.Sprintf("> %v", schemaVersionTargetIdNotSet))
	if err != nil {
		return nil, fmt.Errorf("failed to create check target id constraint: %w", err)
	}

	return &Keysplitting{
		logger:              logger,
		expectedHPointer:    "",
		publickey:           config.GetPublicKey(),
		privatekey:          config.GetPrivateKey(),
		idpProvider:         config.GetIdpProvider(),
		idpOrgId:            config.GetIdpOrgId(),
		shouldCheckTargetId: shouldCheckTargetIdConstraint,
	}, nil
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
		} else {
			k.daemonSchemaVersion = v
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
			return fmt.Errorf("DATA's hash pointer %s did not match expected hash pointer %s", dataPayload.HPointer, k.expectedHPointer)
		}

		k.lastDataMessage = ksMessage
	default:
		return fmt.Errorf("error validating unhandled Keysplitting type")
	}

	return nil
}

func (k *Keysplitting) BuildAck(ksMessage *ksmsg.KeysplittingMessage, action string, actionPayload []byte) (ksmsg.KeysplittingMessage, error) {
	var responseMessage ksmsg.KeysplittingMessage
	var err error

	schemaVersion, err := k.getSchemaVersionToUse()
	if err != nil {
		return responseMessage, err
	}

	switch ksMessage.Type {
	case ksmsg.Syn:
		// If this is the beginning of the hash chain, then we create a nonce with a random value,
		// otherwise we use the hash of the previous value to maintain the hash chain and immutability
		nonce := util.Nonce()
		if k.lastDataMessage != nil {
			hashLastValidDataMessage := k.lastDataMessage.Hash()
			if hashLastValidDataMessage == "" {
				return ksmsg.KeysplittingMessage{}, fmt.Errorf("failed to get hash of last validated data message")
			} else {
				nonce = hashLastValidDataMessage
			}
		}

		responseMessage, err = ksMessage.BuildUnsignedSynAck(actionPayload, k.publickey, nonce, schemaVersion.String())

	case ksmsg.Data:
		responseMessage, err = ksMessage.BuildUnsignedDataAck(actionPayload, k.publickey, schemaVersion.String())
	default:

	}

	if err != nil {
		return responseMessage, err
	} else if err := responseMessage.Sign(k.privatekey); err != nil {
		return responseMessage, fmt.Errorf("could not sign payload: %s", err)
	} else if hash := responseMessage.Hash(); hash == "" {
		return responseMessage, fmt.Errorf("could not hash payload")
	} else {
		k.expectedHPointer = hash
		return responseMessage, nil
	}
}

func (k *Keysplitting) getSchemaVersionToUse() (*semver.Version, error) {
	agentVersion, err := semver.NewVersion(message.SchemaVersion)
	if err != nil {
		return nil, err
	}

	if k.daemonSchemaVersion.LessThan(agentVersion) {
		return k.daemonSchemaVersion, nil
	} else {
		return agentVersion, nil
	}
}
