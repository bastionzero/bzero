package bzcert

import (
	"encoding/base64"
	"fmt"

	"bastionzero.com/bctl/v1/bzerolib/keysplitting/bzcert"
	"bastionzero.com/bctl/v1/bzerolib/keysplitting/bzcert/zliconfig"
)

type IDaemonBZCert interface {
	bzcert.IBZCert
	Cert() *bzcert.BZCert
	PrivateKey() string
	Refresh() error
}

type DaemonBZCert struct {
	bzcert.BZCert

	// unexported members
	privateKey string
	config     *zliconfig.ZLIConfig
}

func New(
	config *zliconfig.ZLIConfig,
) (*DaemonBZCert, error) {

	cert := &DaemonBZCert{
		config: config,
	}

	// Populate our BZCert with values taken from the zli config file
	if err := cert.populateFromConfig(); err != nil {
		return nil, fmt.Errorf("failed to initialize the BastionZero Certificate: %w", err)
	}
	return cert, nil
}

func (b *DaemonBZCert) Cert() *bzcert.BZCert {
	return &b.BZCert
}

func (b *DaemonBZCert) PrivateKey() string {
	return b.privateKey
}

func (b *DaemonBZCert) Refresh() error {
	// Refresh our idp token values using the zli
	if err := b.config.Refresh(); err != nil {
		return err
	}

	if err := b.populateFromConfig(); err != nil {
		return err
	}

	return nil
}

func (b *DaemonBZCert) populateFromConfig() error {
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

	// Finally also check the bzcert is valid
	if err := b.Verify(b.config.CertConfig.OrgProvider, b.config.CertConfig.OrgIssuerId); err != nil {
		return err
	}

	return b.HashCert()
}
