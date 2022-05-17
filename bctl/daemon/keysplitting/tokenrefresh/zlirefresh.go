package tokenrefresh

import (
	"fmt"

	"bastionzero.com/bctl/v1/bzerolib/keysplitting/util"
)

type ZLIKeysplittingTokenRefresher struct {
	refreshTokenCommand string
	configPath          string
}

func NewZLIKeysplittingTokenRefresher(
	refreshTokenCommand string,
	configPath string,
) *ZLIKeysplittingTokenRefresher {
	return &ZLIKeysplittingTokenRefresher{
		refreshTokenCommand: refreshTokenCommand,
		configPath:          configPath,
	}
}

func (r *ZLIKeysplittingTokenRefresher) Refresh() (*ZLIKeysplittingConfig, error) {
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
