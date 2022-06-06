package tokenrefresh

import (
	"fmt"

	"bastionzero.com/bctl/v1/bzerolib/keysplitting/util"
)

type MRZAPTokenRefresher struct {
	configPath          string
	refreshTokenCommand string
}

func NewMRZAPTokenRefresher(
	configPath string,
	refreshTokenCommand string,
) *MRZAPTokenRefresher {
	return &MRZAPTokenRefresher{
		configPath:          configPath,
		refreshTokenCommand: refreshTokenCommand,
	}
}

func (r *MRZAPTokenRefresher) Refresh() (*ZLIKeysplittingConfig, error) {
	// Update the id token by calling the passed in zli refresh command
	if err := util.RunRefreshAuthCommand(r.refreshTokenCommand); err != nil {
		return nil, fmt.Errorf("failed to run refresh auth command: %w", err)
	}
	// Then re-load the config file
	zliKSConfig, err := loadZLIKeysplittingConfig(r.configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load zli keysplitting config: %w", err)
	}
	return zliKSConfig, nil
}
