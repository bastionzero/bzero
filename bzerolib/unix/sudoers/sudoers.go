package sudoers

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	defaultSudoersDirPath  = "/etc/sudoers.d"
	defaultSudoersFileName = "bastionzero-users"
	sudoersFilePermissions = 0640
)

type ISudoersFile interface {
	AddUser(string) error
}

type SudoersFile struct {
	filePath string
}

func New(filePath string) *SudoersFile {
	return &SudoersFile{filePath: filePath}
}

func NewDefault() *SudoersFile {
	return &SudoersFile{filePath: filepath.Join(defaultSudoersDirPath, defaultSudoersFileName)}
}

// adds the given user to the given sudoers file if the user is not there already
func (s *SudoersFile) AddUser(username string) error {
	// first check if the user is in the file
	if sudoers, err := s.parse(); err != nil {
		return err
	} else if _, ok := sudoers[username]; !ok {
		// if not, add them
		return s.append(username)
	}
	// if the user is there, we're all set
	return nil
}

// return a set of users (and groups, in theory) in a sudoers file
func (s *SudoersFile) parse() (map[string]struct{}, error) {
	result := make(map[string]struct{})

	fileBytes, err := os.ReadFile(s.filePath)
	if errors.Is(err, os.ErrNotExist) {
		// if the file doesn't exist, there are no sudoers in it!
		return result, nil
	} else if err != nil {
		return nil, fmt.Errorf("failed to read from sudoers file \"%s\": %s", s.filePath, err)
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

// adds the given user to the end of the given sudoers file; creates the file if not exists
func (s *SudoersFile) append(username string) error {
	sudoersEntry := fmt.Sprintf("%s ALL=(ALL) NOPASSWD:ALL\n", username)

	if file, err := os.OpenFile(s.filePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, sudoersFilePermissions); err != nil {
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
