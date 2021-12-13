package vault

import (
	"bytes"
	"context"
	"encoding/gob"
	"fmt"
	"os"

	coreV1 "k8s.io/api/core/v1"
	metaV1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	coreV1Types "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"
)

const (
	keyConfig = "keyConfig"
)

type Vault struct {
	client coreV1Types.SecretInterface
	secret *coreV1.Secret
	Data   SecretData
}

type SecretData struct {
	PublicKey   string
	PrivateKey  string
	OrgId       string
	ServiceUrl  string
	ClusterName string
	Namespace   string
	IdpProvider string
	IdpOrgId    string
}

func LoadVault() (*Vault, error) {
	// Create our api object
	config, err := rest.InClusterConfig()
	if err != nil {
		return &Vault{}, fmt.Errorf("error grabbing cluster config: %v", err.Error())
	}

	if clientset, err := kubernetes.NewForConfig(config); err != nil {
		return &Vault{}, fmt.Errorf("error creating new config: %v", err.Error())
	} else {
		secretName := "bctl-" + os.Getenv("CLUSTER_NAME") + "-secret"

		// Create our secrets client
		secretsClient := clientset.CoreV1().Secrets(os.Getenv("NAMESPACE"))

		// Get our secrets object
		if secret, err := secretsClient.Get(context.Background(), secretName, metaV1.GetOptions{}); err != nil {
			// If there is no secret there, create it
			secretData := map[string][]byte{
				"secret": []byte("coolbeans"),
			}
			object := metaV1.ObjectMeta{Name: secretName}
			secret := &coreV1.Secret{Data: secretData, ObjectMeta: object}

			if _, err := secretsClient.Create(context.TODO(), secret, metaV1.CreateOptions{}); err != nil {
				return &Vault{}, fmt.Errorf("error creating secret: %v", err.Error())
			}

			return &Vault{
				client: secretsClient,
				secret: secret,
				Data:   SecretData{},
			}, nil

		} else {
			if data, ok := secret.Data[keyConfig]; ok {
				if secretData, err := DecodeToSecretConfig(data); err != nil {
					return &Vault{}, err
				} else {
					return &Vault{
						client: secretsClient,
						secret: secret,
						Data:   secretData,
					}, nil
				}
			} else {
				secretsClient := clientset.CoreV1().Secrets(os.Getenv("NAMESPACE"))
				return &Vault{
					client: secretsClient,
					secret: secret,
					Data:   SecretData{},
				}, nil
			}

		}
	}
}

func (v *Vault) IsEmpty() bool {
	if v.Data == (SecretData{}) {
		return true
	} else {
		return false
	}
}

func (v *Vault) Save() error {
	// Now encode the secretConfig
	encodedSecretConfig, err := EncodeToBytes(v.Data)
	if err != nil {
		return err
	}

	// Now update the kube secret object
	v.secret.Data[keyConfig] = encodedSecretConfig

	// Update the secret
	if _, err := v.client.Update(context.Background(), v.secret, metaV1.UpdateOptions{}); err != nil {
		return fmt.Errorf("could not update secret client: %v", err.Error())
	} else {
		return nil
	}
}

func EncodeToBytes(p interface{}) ([]byte, error) {
	// Ref: https://gist.github.com/SteveBate/042960baa7a4795c3565
	// Remove secrets client
	buf := bytes.Buffer{}
	enc := gob.NewEncoder(&buf)
	err := enc.Encode(p)
	return buf.Bytes(), err
}

func DecodeToSecretConfig(s []byte) (SecretData, error) {
	// Ref: https://gist.github.com/SteveBate/042960baa7a4795c3565
	p := SecretData{}
	dec := gob.NewDecoder(bytes.NewReader(s))
	err := dec.Decode(&p)
	return p, err
}
