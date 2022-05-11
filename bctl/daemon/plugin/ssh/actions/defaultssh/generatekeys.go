package defaultssh

// source: https://gist.github.com/devinodaniel/8f9b8a4f31573f428f29ec0e884e6673

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"os"

	"golang.org/x/crypto/ssh"
)

const bitSize int = 4096

func GenerateKeys() ([]byte, []byte, error) {
	privateKey, err := generatePrivateKey(bitSize)
	if err != nil {
		return nil, nil, err
	}

	publicKeyBytes, err := generatePublicKey(&privateKey.PublicKey)
	if err != nil {
		return nil, nil, err
	}

	privateKeyBytes := encodePrivateKeyToPem(privateKey)

	return privateKeyBytes, publicKeyBytes, nil
}

// generatePrivateKey creates a RSA Private Key of specified byte size
func generatePrivateKey(bitSize int) (*rsa.PrivateKey, error) {
	// Private Key generation
	privateKey, err := rsa.GenerateKey(rand.Reader, bitSize)
	if err != nil {
		return nil, err
	}

	// Validate Private Key
	err = privateKey.Validate()
	if err != nil {
		return nil, err
	}

	return privateKey, nil
}

// RSA -> PEM
func encodePrivateKeyToPem(privateKey *rsa.PrivateKey) []byte {
	// Get ASN.1 DER format
	privateDer := x509.MarshalPKCS1PrivateKey(privateKey)

	// pem.Block
	privateBlock := pem.Block{
		Type:    "RSA PRIVATE KEY",
		Headers: nil,
		Bytes:   privateDer,
	}

	// Private key in PEM format
	privatePem := pem.EncodeToMemory(&privateBlock)

	return privatePem
}

// PEM -> RSA
func decodePemToPrivateKey(privatePem []byte) (*rsa.PrivateKey, error) {
	privateBlock, _ := pem.Decode(privatePem)
	return x509.ParsePKCS1PrivateKey(privateBlock.Bytes)
}

// generatePublicKey take a rsa.PublicKey and return bytes suitable for writing to .pub file
// returns in the format "ssh-rsa ..."
func generatePublicKey(publicKey *rsa.PublicKey) ([]byte, error) {
	publicRsaKey, err := ssh.NewPublicKey(publicKey)
	if err != nil {
		return nil, err
	}

	pubKeyBytes := ssh.MarshalAuthorizedKey(publicRsaKey)

	return pubKeyBytes, nil
}

// takes a private key path and returns a public key struct
// returns an error if the key cannot be read or is invalid
func readPublicKeyRsa(privateKeyPath string) (*rsa.PublicKey, error) {
	if privatePem, err := os.ReadFile(privateKeyPath); err != nil {
		return nil, err
	} else if privateKey, err := decodePemToPrivateKey(privatePem); err != nil {
		return nil, err
	} else {
		return &privateKey.PublicKey, privateKey.Validate()
	}
}
