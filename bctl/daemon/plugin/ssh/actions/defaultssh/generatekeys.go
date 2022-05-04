package defaultssh

// source: https://gist.github.com/devinodaniel/8f9b8a4f31573f428f29ec0e884e6673

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io/ioutil"
	"log"

	"golang.org/x/crypto/ssh"
)

const bitSize int = 4096

// TODO: separate out into generating and writing...
func GenerateKeys(identityFile string) ([]byte, error) {
	privateKey, err := generatePrivateKey(bitSize)
	if err != nil {
		return nil, err
	}

	publicKeyBytes, err := generatePublicKey(&privateKey.PublicKey)
	if err != nil {
		return nil, err
	}

	privateKeyBytes := encodePrivateKeyToPEM(privateKey)

	err = writeKeyToFile(privateKeyBytes, identityFile)
	if err != nil {
		return nil, err
	}

	err = writeKeyToFile(publicKeyBytes, fmt.Sprintf("%s.pub", identityFile))
	if err != nil {
		return nil, err
	}

	return publicKeyBytes, nil
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

	log.Println("Private Key generated")
	return privateKey, nil
}

// encodePrivateKeyToPEM encodes Private Key from RSA to PEM format
func encodePrivateKeyToPEM(privateKey *rsa.PrivateKey) []byte {
	// Get ASN.1 DER format
	privDER := x509.MarshalPKCS1PrivateKey(privateKey)

	// pem.Block
	privBlock := pem.Block{
		Type:    "RSA PRIVATE KEY",
		Headers: nil,
		Bytes:   privDER,
	}

	// Private key in PEM format
	privatePEM := pem.EncodeToMemory(&privBlock)

	return privatePEM
}

// generatePublicKey take a rsa.PublicKey and return bytes suitable for writing to .pub file
// returns in the format "ssh-rsa ..."
func generatePublicKey(privatekey *rsa.PublicKey) ([]byte, error) {
	publicRsaKey, err := ssh.NewPublicKey(privatekey)
	if err != nil {
		return nil, err
	}

	pubKeyBytes := ssh.MarshalAuthorizedKey(publicRsaKey)

	log.Println("Public key generated")
	return pubKeyBytes, nil
}

// writePemToFile writes keys to a file
func writeKeyToFile(keyBytes []byte, saveFileTo string) error {
	err := ioutil.WriteFile(saveFileTo, keyBytes, 0600)
	if err != nil {
		return err
	}

	log.Printf("Key saved to: %s", saveFileTo)
	return nil
}
