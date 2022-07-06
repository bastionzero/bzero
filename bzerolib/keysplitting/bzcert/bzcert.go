package bzcert

import (
	"encoding/base64"
	"fmt"
	"time"

	"bastionzero.com/bctl/v1/bzerolib/keysplitting/bzcert/zliconfig"
	"bastionzero.com/bctl/v1/bzerolib/keysplitting/util"
)

type IBZCert interface {
	Hash() string
	PrivateKey() string
	Expiration() time.Time
}

type BZCert struct {
	InitialIdToken  string `json:"initialIdToken"`
	CurrentIdToken  string `json:"currentIdToken"`
	ClientPublicKey string `json:"clientPublicKey"`
	Rand            string `json:"rand"`
	SignatureOnRand string `json:"signatureOnRand"`

	// unexported members
	config     *zliconfig.ZLIConfig
	privateKey string
	expiration time.Time
	hash       string
}

func New(config *zliconfig.ZLIConfig) (*BZCert, error) {
	cert := &BZCert{
		config: config,
	}

	// Populate our BZCert with values taken from the zli config file
	if err := cert.Refresh(); err != nil {
		return nil, fmt.Errorf("failed to initialize the BastionZero Certificate: %w", err)
	}
	return cert, nil
}

func (b *BZCert) Hash() string {
	return b.hash
}

func (b *BZCert) PrivateKey() string {
	return b.privateKey
}

func (b *BZCert) Expired() bool {
	return time.Now().After(b.expiration)
}

// This function verifies the user's bzcert. The function returns the hash the bzcert, the
// expiration time of the bzcert, and an error if there is one
func (b *BZCert) Verify(idpProvider string, idpOrgId string) error {
	// initialize a new verifier for BastionZero certificates
	if verifier, err := NewVerifier(idpProvider, idpOrgId); err != nil {
		return fmt.Errorf("error initializing bzcert verifier: %w", err)
	} else if exp, err := verifier.Verify(b); err != nil {
		return fmt.Errorf("failed to verify the BastionZero certificate: %w", err)
	} else if err := b.hashCert(); err != nil {
		return err
	} else {
		b.expiration = exp
	}

	return nil
}

func (b *BZCert) Refresh() error {
	// Refresh our idp token values using the zli
	if err := b.config.Refresh(); err != nil {
		return err
	}

	var privateKey string
	if privateKeyBytes, err := base64.StdEncoding.DecodeString(b.config.CertConfig.PrivateKey); err != nil {
		return fmt.Errorf("failed to base64 decode private key: %w", err)
	} else if len(privateKeyBytes) == 64 {
		// The golang ed25519 library uses a length 64 private key because the
		// private key is in the concatenated form privatekey = privatekey + publickey.
		privateKey = b.config.CertConfig.PrivateKey
	} else if len(privateKeyBytes) == 32 {
		// If the key was generated as length 32, we can correct for that here
		publickeyBytes, _ := base64.StdEncoding.DecodeString(b.config.CertConfig.PublicKey)
		privateKey = base64.StdEncoding.EncodeToString(append(privateKeyBytes, publickeyBytes...))
	} else {
		return fmt.Errorf("malformatted private key of incorrect length: %d", len(privateKeyBytes))
	}

	// Update all of our objects values
	b.InitialIdToken = b.config.CertConfig.InitialIdToken
	b.CurrentIdToken = b.config.TokenSet.CurrentIdToken
	b.ClientPublicKey = b.config.CertConfig.PublicKey
	b.Rand = b.config.CertConfig.CerRand
	b.SignatureOnRand = b.config.CertConfig.CerRandSignature
	b.privateKey = privateKey

	return b.hashCert()
}

func (b *BZCert) hashCert() error {
	if hashBytes, ok := util.HashPayload(*b); !ok {
		return fmt.Errorf("failed to hash the BastionZero certificate")
	} else {
		b.hash = base64.StdEncoding.EncodeToString(hashBytes)
		return nil
	}
}
