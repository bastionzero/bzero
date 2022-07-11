package unixuser

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

const (
	sudoersFilePermissions = 0640
)

// adds the given user to the given sudoers file if the user is not there already
func addToSudoers(username string, sudoersFilePath string) error {
	// first check if the user is in the file
	if sudoers, err := parseSudoersFile(sudoersFilePath); err != nil {
		return err
	} else if _, ok := sudoers[username]; !ok {
		// if not, add them
		return appendSudoersFile(username, sudoersFilePath)
	}
	// if the user is there, we're all set
	return nil
}

// adds the given user to the end of the given sudoers file; creates the file if not exists
func appendSudoersFile(username string, sudoersFilePath string) error {
	sudoersEntry := fmt.Sprintf("%s ALL=(ALL) NOPASSWD:ALL\n", username)

	// open the file as the current user so that we can bubble up any permissions errors
	if usr, err := Current(); err != nil {
		return fmt.Errorf("failed to determine current user")
	} else if file, err := usr.OpenFile(sudoersFilePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, sudoersFilePermissions); err != nil {
		return err
	} else {
		// if the file was previously empty, then we make sure to add a comment
		if fi, err := file.Stat(); err == nil && fi.Size() == 0 {
			sudoersFileComment := fmt.Sprintf("# Created by BastionZero on %s\n\n", time.Now().Round(time.Second))
			if _, err := file.WriteString(sudoersFileComment); err != nil {
				return err
			}
		}
		// add our sudoers entry
		if _, err := file.WriteString(sudoersEntry); err != nil {
			return err
		}
	}
	return nil
}

// return a set of users (and groups, in theory) in a sudoers file
func parseSudoersFile(sudoersFilePath string) (map[string]struct{}, error) {
	result := make(map[string]struct{})

	fileBytes, err := os.ReadFile(sudoersFilePath)
	if errors.Is(err, os.ErrNotExist) {
		// if the file doesn't exist, there are no sudoers in it!
		return result, nil
	} else if err != nil {
		return nil, fmt.Errorf("failed to read from sudoers file \"%s\": %s", sudoersFilePath, err)
	}

	scanner := bufio.NewScanner(bytes.NewReader(fileBytes))
	for scanner.Scan() {
		entry := strings.TrimSpace(scanner.Text())

		// ignore blank lines
		if len(entry) == 0 {
			continue
		}

		// ignore comments and include directives
		if string(entry[0]) == "#" {
			continue
		}

		result[strings.Fields(entry)[0]] = struct{}{}
	}
	return result, nil
}
