package authorizedkeys

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"bastionzero.com/bctl/v1/bzerolib/filelock"
	"bastionzero.com/bctl/v1/bzerolib/logger"
	"bastionzero.com/bctl/v1/bzerolib/unix/unixuser"
)

const (
	authorizedKeyComment         = "bzero-temp-key"
	lockFileName                 = ".bzero.lock"
	authorizedKeyFileName        = "authorized_keys"
	authorizedKeysFilePermission = 0600 // only owner (user) can read/write
	authorizedKeysDirPermission  = 0700 // only owner (user) can read/read/execute
)

type IAuthorizedKeys interface {
	Add(pubkey string) error
}

type AuthorizedKeys struct {
	logger   *logger.Logger
	doneChan chan struct{}

	keyLifetime time.Duration

	usr *unixuser.UnixUser

	keyFilePath string
	fileLock    *filelock.FileLock
}

func New(
	logger *logger.Logger,
	doneChan chan struct{},
	usr *unixuser.UnixUser,
	authKeyFolder string,
	lockFileFolder string,
	keyLifetime time.Duration,
) (*AuthorizedKeys, error) {

	if usr.HomeDir == "" {
		return nil, fmt.Errorf("user does not have an associated home directory")
	}

	authKey := &AuthorizedKeys{
		logger:      logger,
		doneChan:    doneChan,
		keyLifetime: keyLifetime,
		usr:         usr,
	}

	if err := authKey.setKeyFilePath(authKeyFolder); err != nil {
		return nil, err
	} else if err := authKey.setFileLock(usr.HomeDir, lockFileFolder); err != nil {
		return nil, err
	} else {
		return authKey, nil
	}
}

func (a *AuthorizedKeys) Add(pubkey string) error {
	// build authorized key entry in the right format then add it to the file
	if entry, err := a.buildAuthorizedKeyEntry(pubkey); err != nil {
		return err
	} else if err := a.addKeyToFile(entry); err != nil {
		return fmt.Errorf("failed to add key to authorized_keys file: %s", err)
	} else {

		// remove the key since it's ephemeral
		go func() {
			select {
			case <-a.doneChan:
				a.logger.Infof("Detected a closed done chan, cleaning authorized keys file")
			case <-time.After(a.keyLifetime):
				a.logger.Infof("SSH key expired, cleaning authorized keys file")
			}

			if err := a.cleanAuthorizedKeys(entry); err != nil {
				a.logger.Errorf("Failed to remove old keys from %s: %s", a.keyFilePath, err)
			}
		}()
	}

	return nil
}

func (a *AuthorizedKeys) buildAuthorizedKeyEntry(pubkey string) (string, error) {
	// Construct the authorized key entry
	// Assumes for now only ssh-rsa key types will be generated by the client so we do not need to validate the key type
	keyData := strings.Fields(pubkey)
	if len(keyData) < 2 {
		return "", fmt.Errorf("malformed public key")
	}

	keyType := keyData[0]
	keyContents := keyData[1]

	// test that the provided public key is valid base64 data
	if _, err := base64.StdEncoding.DecodeString(keyContents); err != nil {
		return "", fmt.Errorf("invalid public key provided: %s", keyContents)
	}

	timestamp := time.Now().UnixNano()
	key := fmt.Sprintf("%s %s %s created_at=%d", keyType, keyContents, authorizedKeyComment, timestamp)
	return key, nil
}

func (a *AuthorizedKeys) addKeyToFile(contents string) error {
	// wait to acquire a lock on the authorized_keys file
	lock, err := a.fileLock.NewLock()
	if err != nil {
		return fmt.Errorf("failed to obtain lock: %s", err)
	}
	for {
		if acquiredLock, err := lock.TryLock(); err != nil {
			return fmt.Errorf("error acquiring lock: %s", err)
		} else if acquiredLock {
			break
		}
	}

	defer lock.Unlock()

	file, err := a.usr.OpenFile(a.keyFilePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, authorizedKeysFilePermission)
	if err != nil {
		return err
	}
	defer file.Close()

	// make sure we're always writing our keys to new lines
	// this read of the file is so that the key entry can be built/formatted properly and is,
	//  therefore a function of the process and not the user
	if fileBytes, err := os.ReadFile(a.keyFilePath); err != nil {
		return err
	} else if len(fileBytes) > 0 && !strings.HasSuffix(string(fileBytes), "\n") {
		contents = "\n" + contents
	}

	if _, err := file.WriteString(contents); err != nil {
		return err
	}

	return nil
}

// this function acts on behalf of the process, any file system reads are a result of the process
// cleaning up the file and not the user attempting to delete the key themselves
func (a *AuthorizedKeys) cleanAuthorizedKeys(currentKey string) error {
	// wait to acquire a lock on the authorized_keys file
	lock, err := a.fileLock.NewLock()
	if err != nil {
		return fmt.Errorf("failed to obtain lock: %s", err)
	}
	for {
		if acquiredLock, err := lock.TryLock(); err != nil {
			return fmt.Errorf("error acquiring lock: %s", err)
		} else if acquiredLock {
			break
		}
	}

	defer lock.Unlock()

	// read the authorized key file
	fileBytes, err := os.ReadFile(a.keyFilePath)
	if err != nil {
		return fmt.Errorf("failed to read from authorized_keys file \"%s\": %s", a.keyFilePath, err)
	}

	// iterate over the lines in our file, each line presents a new authorized_key entry
	newFileBytes := []byte{}
	scanner := bufio.NewScanner(bytes.NewReader(fileBytes))
	for scanner.Scan() {
		key := scanner.Text()

		// remove it if it's our current key, even if it's unexpired
		if key == currentKey {
			continue
		} else if strings.Contains(key, authorizedKeyComment) && strings.Contains(key, "created_at=") {
			// parse our creation time
			creationTimeString := strings.Split(strings.Split(key, "created_at=")[1], " ")[0]
			if creationTimeInt, err := strconv.ParseInt(creationTimeString, 10, 64); err != nil {
				// By returning here, we are opting not to overwrite the file at all.
				// This potentially leaves it around for longer but mitigates the risk of accidentally erasing something important
				return fmt.Errorf("malformated unix time")
			} else {
				unixCreationTime := time.Unix(0, creationTimeInt)

				// if the key is expired, remove it
				if time.Since(unixCreationTime) >= a.keyLifetime {
					continue
				}
			}
		}

		// scanner.Scan removed new line so we add that back in
		newLine := append(scanner.Bytes(), "\n"...)
		newFileBytes = append(newFileBytes, newLine...)
	}

	if err := scanner.Err(); err != nil {
		return err
	}

	// we don't need to check permissions here, because it's the agent's responsibility
	// to clean the authorized_keys file, not the user's
	if err := os.WriteFile(a.keyFilePath, newFileBytes, authorizedKeysFilePermission); err != nil {
		return err
	}
	return nil
}

// validate that the user has a home directory, drop down to their permissions, and create authorized_keys if not exists
func (a *AuthorizedKeys) setKeyFilePath(keyFolder string) error {
	path := filepath.Join(a.usr.HomeDir, keyFolder)

	if err := a.usr.Mkdir(path, authorizedKeysDirPermission); err != nil {
		return err
	} else {
		a.keyFilePath = filepath.Join(path, authorizedKeyFileName)
	}

	return nil
}

func (a *AuthorizedKeys) setFileLock(homeDir string, lockFileFolder string) error {
	path := filepath.Join(homeDir, lockFileFolder)

	// since the file lock is for the benefit of the agent and not the user, it is the agent's
	// responsibility to create it. The user never directly interacts with it.
	if err := os.MkdirAll(path, os.ModePerm); err != nil {
		return fmt.Errorf("failed to create %s: %s", path, err)
	} else {
		a.fileLock = filelock.NewFileLock(filepath.Join(path, lockFileName))
		return nil
	}
}
