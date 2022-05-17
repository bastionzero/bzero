package tokenrefresh

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
)

type ZLIKeysplittingConfig struct {
	KSConfig KeysplittingConfig `json:"keySplitting"`
	TokenSet ZLITokenSetConfig  `json:"tokenSet"`
}
type ZLITokenSetConfig struct {
	CurrentIdToken string `json:"id_token"`
}

type KeysplittingConfig struct {
	PrivateKey       string `json:"privateKey"`
	PublicKey        string `json:"publicKey"`
	CerRand          string `json:"cerRand"`
	CerRandSignature string `json:"cerRandSig"`
	InitialIdToken   string `json:"initialIdToken"`
}

func loadZLIKeysplittingConfig(configPath string) (*ZLIKeysplittingConfig, error) {
	var config ZLIKeysplittingConfig

	if configFile, err := os.Open(configPath); err != nil {
		return nil, fmt.Errorf("could not open config file: %w", err)
	} else if configFileBytes, err := ioutil.ReadAll(configFile); err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	} else if err := json.Unmarshal(configFileBytes, &config); err != nil {
		return nil, fmt.Errorf("could not unmarshal config file: %w", err)
	} else {
		return &config, nil
	}
}
