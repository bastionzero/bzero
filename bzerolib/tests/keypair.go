package tests

import (
	ed "crypto/ed25519"
	"encoding/base64"
	"fmt"
)

type Ed25519KeyPair struct {
	PublicKey               ed.PublicKey
	PrivateKey              ed.PrivateKey
	Base64EncodedPublicKey  string
	Base64EncodedPrivateKey string
}

func GenerateEd25519Key() (*Ed25519KeyPair, error) {
	publicKey, privateKey, err := ed.GenerateKey(nil)
	if err != nil {
		return nil, fmt.Errorf("failed to generate ed25519 key pair: %w", err)
	}
	return &Ed25519KeyPair{
		PublicKey:               publicKey,
		PrivateKey:              privateKey,
		Base64EncodedPublicKey:  base64.StdEncoding.EncodeToString([]byte(publicKey)),
		Base64EncodedPrivateKey: base64.StdEncoding.EncodeToString([]byte(privateKey)),
	}, nil
}
