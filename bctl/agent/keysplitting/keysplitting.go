package keysplitting

import (
	"fmt"
	"time"

	bzcrt "bastionzero.com/bctl/v1/bzerolib/keysplitting/bzcert"
	ksmsg "bastionzero.com/bctl/v1/bzerolib/keysplitting/message"
	"bastionzero.com/bctl/v1/bzerolib/keysplitting/util"
	"github.com/Masterminds/semver"
)

// schema version <= this value doesn't set targetId to the agent's pubkey
const schemaVersionTargetIdNotSet string = "1.0"

type bzCertMetadata struct {
	Hash       string
	Cert       bzcrt.BZCert
	Expiration time.Time
}

type BZCertVerifier interface {
	Verify(bzCert bzcrt.BZCert) (hash string, exp time.Time, err error)
}

type IKeysplittingConfig interface {
	GetPublicKey() string
	GetPrivateKey() string
	GetIdpProvider() string
	GetIdpOrgId() string
}

type Keysplitting struct {
	lastDataMessage  *ksmsg.KeysplittingMessage
	expectedHPointer string
	bzCert           bzCertMetadata // only for one client
	publickey        string
	privatekey       string

	bzCertVerifier BZCertVerifier

	agentSchemaVersion *semver.Version

	// define constraints based on schema version
	shouldCheckTargetId *semver.Constraints
	daemonSchemaVersion *semver.Version
}

type KeysplittingParameters struct {
	// Config contains the agent's keysplitting configuration. If unset, New()
	// returns an error.
	Config IKeysplittingConfig
	// Verifier is the verifier to use when validating the BZCert parsed from
	// Syn messages. If unset, New() uses the verifier defined in BzeroLib and
	// configures it using parameters queried from the Config.
	Verifier BZCertVerifier
	// SchemaVersion is the schema version the agent uses when building ack
	// messages (SynAck and DataAck) when the daemon's schema version is greater
	// than or equal to this value. If unset, New() uses the schema version
	// defined in BzeroLib.
	SchemaVersion string
}

func New(parameters KeysplittingParameters) (*Keysplitting, error) {
	keysplitter := &Keysplitting{}

	// Validate Config
	if parameters.Config == nil {
		return nil, fmt.Errorf("invalid parameters: Config field must be set")
	} else {
		keysplitter.publickey = parameters.Config.GetPublicKey()
		keysplitter.privatekey = parameters.Config.GetPrivateKey()
	}

	// Validate Verifier
	if parameters.Verifier == nil {
		if verifier, err := bzcrt.NewBZCertVerifier(parameters.Config.GetIdpProvider(), parameters.Config.GetIdpOrgId()); err != nil {
			return nil, fmt.Errorf("failed to init BZCertVerifier: %w", err)
		} else {
			keysplitter.bzCertVerifier = verifier
		}
	}

	// Validate SchemaVersion
	var schemaVersion string
	if parameters.SchemaVersion == "" {
		schemaVersion = ksmsg.SchemaVersion
	} else {
		schemaVersion = parameters.SchemaVersion
	}
	agentSchemaVersion, err := semver.NewVersion(schemaVersion)
	if err != nil {
		return nil, fmt.Errorf("failed to parse schema version: %w", err)
	}
	keysplitter.agentSchemaVersion = agentSchemaVersion

	// Setup other required fields that aren't derived from parameters struct
	shouldCheckTargetIdConstraint, err := semver.NewConstraint(fmt.Sprintf("> %v", schemaVersionTargetIdNotSet))
	if err != nil {
		return nil, fmt.Errorf("failed to create check target id constraint: %w", err)
	}
	keysplitter.shouldCheckTargetId = shouldCheckTargetIdConstraint
	keysplitter.expectedHPointer = ""

	return keysplitter, nil
}

func (k *Keysplitting) Validate(ksMessage *ksmsg.KeysplittingMessage) error {
	switch ksMessage.Type {
	case ksmsg.Syn:
		synPayload := ksMessage.KeysplittingPayload.(ksmsg.SynPayload)

		// Verify the BZCert
		hash, exp, err := k.bzCertVerifier.Verify(synPayload.BZCert)
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
		k.bzCert = bzCertMetadata{
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
			if hpointer, err := k.lastDataMessage.GetHpointer(); err != nil {
				return ksmsg.KeysplittingMessage{}, fmt.Errorf("failed to get hpointer of last ack: %s", err)
			} else {
				nonce = hpointer
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
	if k.daemonSchemaVersion.LessThan(k.agentSchemaVersion) {
		return k.daemonSchemaVersion, nil
	} else {
		return k.agentSchemaVersion, nil
	}
}
