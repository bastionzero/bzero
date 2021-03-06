package vault

import (
	"bytes"
	"context"
	"encoding/gob"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"sync"

	"bastionzero.com/bctl/v1/bzerolib/logger"
	"github.com/fsnotify/fsnotify"
	coreV1 "k8s.io/api/core/v1"
	metaV1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	coreV1Types "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"
)

const (
	vaultKey          = "secret"
	defaultValueValue = "coolbeans"

	// Env var to flag if we are in a kube cluster
	inClusterEnvVar = "BASTIONZERO_IN_CLUSTER"

	// Vault systemd consts
	vaultPath = "/etc/bzero/vault.json"
)

type Vault struct {
	Data SecretData

	// Kube secret related
	client   coreV1Types.SecretInterface
	secret   *coreV1.Secret
	fileLock sync.Mutex
}

type SecretData struct {
	PublicKey       string
	PrivateKey      string
	ServiceUrl      string
	TargetName      string
	Namespace       string
	IdpProvider     string
	IdpOrgId        string
	TargetId        string
	EnvironmentId   string
	EnvironmentName string
	AgentType       string
	Version         string
}

func LoadVault() (*Vault, error) {
	if InCluster() {
		// This means we are in the cluster
		return clusterVault()
	} else {
		return systemdVault()
	}
}

func InCluster() bool {
	if val := os.Getenv(inClusterEnvVar); val == "bzero" {
		return true
	} else {
		return false
	}
}

func WaitForNewRegistration(logger *logger.Logger) error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("error starting new file watcher: %s", err)
	}
	defer watcher.Close()

	done := make(chan error)
	go func() {
		for {
			select {
			case event, ok := <-watcher.Events:
				if !ok {
					done <- fmt.Errorf("file watcher closed events channel")
				}

				if event.Op&fsnotify.Write == fsnotify.Write {
					var config SecretData
					if file, err := ioutil.ReadFile(vaultPath); err != nil {
						continue
					} else if err := json.Unmarshal([]byte(file), &config); err != nil {
						continue
					} else {
						// if we haven't completed registration yet, continue waiting
						if config.PublicKey == "" {
							continue
						} else {
							done <- nil
						}
					}
				}
			case err, ok := <-watcher.Errors:
				if !ok {
					done <- fmt.Errorf("file watcher closed errors channel")
				}
				done <- fmt.Errorf("file watcher caught error: %s", err)
			}
		}
	}()

	if err := watcher.Add(vaultPath); err != nil {
		return fmt.Errorf("unable to watch file: %s, error: %s", vaultPath, err)
	}

	return <-done
}

func systemdVault() (*Vault, error) {
	var secretData SecretData

	// check if file exists
	if f, err := os.Stat(vaultPath); os.IsNotExist(err) { // our file does not exist

		// create our directory, if it doesn't exit
		if err := os.MkdirAll(filepath.Dir(vaultPath), os.ModePerm); err != nil {
			return nil, err
		}

		// create our file
		if _, err := os.Create(vaultPath); err != nil {
			return nil, err
		} else {
			vault := Vault{
				client: nil,
				secret: nil,
				Data:   secretData,
			}
			vault.Save()

			// return our newly created, and empty vault
			return &vault, nil
		}
	} else if err != nil {
		return nil, err
	} else if f.Size() == 0 { // our file exists, but is empty
		vault := Vault{
			client: nil,
			secret: nil,
			Data:   secretData,
		}
		vault.Save()

		// return our newly created, and empty vault
		return &vault, nil
	}

	// if the file does exist, read it into memory
	if file, err := ioutil.ReadFile(vaultPath); err != nil {
		return nil, err
	} else if err := json.Unmarshal([]byte(file), &secretData); err != nil {
		return nil, err
	} else {
		return &Vault{
			client: nil,
			secret: nil,
			Data:   secretData,
		}, nil
	}
}

func clusterVault() (*Vault, error) {
	// Create our api object
	config, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("error grabbing cluster config: %v", err.Error())
	}

	if clientset, err := kubernetes.NewForConfig(config); err != nil {
		return nil, fmt.Errorf("error creating new config: %v", err.Error())
	} else {
		secretName := "bctl-" + os.Getenv("TARGET_NAME") + "-secret"

		// Create our secrets client
		secretsClient := clientset.CoreV1().Secrets(os.Getenv("NAMESPACE"))

		// Get our secrets object
		if secret, err := secretsClient.Get(context.Background(), secretName, metaV1.GetOptions{}); err != nil {
			// If there is no secret there, create it
			secretData := map[string][]byte{
				"secret": []byte(defaultValueValue),
			}
			object := metaV1.ObjectMeta{Name: secretName}
			secret := &coreV1.Secret{Data: secretData, ObjectMeta: object}

			if _, err := secretsClient.Create(context.TODO(), secret, metaV1.CreateOptions{}); err != nil {
				return nil, fmt.Errorf("error creating secret: %v", err.Error())
			}

			return &Vault{
				client: secretsClient,
				secret: secret,
				Data:   SecretData{},
			}, nil

		} else {
			if data, ok := secret.Data[vaultKey]; ok {
				if bytes.Equal(data, []byte(defaultValueValue)) {
					// This is a fresh secret, return an empty secrets data
					return &Vault{
						client: secretsClient,
						secret: secret,
						Data:   SecretData{},
					}, nil
				}
				if secretData, err := DecodeToSecretConfig(data); err != nil {
					return nil, err
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
	if InCluster() {
		// This means we are in the cluster
		return v.saveCluster()
	} else {
		return v.saveSystemd()
	}
}

func (v *Vault) GetPublicKey() string {
	return v.Data.PublicKey
}
func (v *Vault) GetPrivateKey() string {
	return v.Data.PrivateKey
}
func (v *Vault) GetIdpProvider() string {
	return v.Data.IdpProvider
}

func (v *Vault) GetIdpOrgId() string {
	return v.Data.IdpOrgId
}

// There is no selective saving, saving the vault will overwrite anything existing
func (v *Vault) saveSystemd() error {
	v.fileLock.Lock()
	defer v.fileLock.Unlock()

	// overwrite entire file every time
	dataBytes, _ := json.Marshal(v.Data)

	// empty out our file
	if err := os.Truncate(vaultPath, 0); err != nil {
		return err
	}

	// replace it with our new vault
	if err := ioutil.WriteFile(vaultPath, dataBytes, 0644); err != nil {
		return err
	}

	return nil
}

func (v *Vault) saveCluster() error {
	// Now encode the secretConfig
	encodedSecretConfig, err := EncodeToBytes(v.Data)
	if err != nil {
		return err
	}

	// Now update the kube secret object
	v.secret.Data[vaultKey] = encodedSecretConfig

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
