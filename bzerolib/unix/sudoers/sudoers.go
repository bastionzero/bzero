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

type ISudoerFile interface {
	AddUser(string) error
}

type SudoerFile struct {
	dirPath  string
	fileName string
}

func New(dirPath string, fileName string) *SudoerFile {
	return &SudoerFile{
		dirPath:  dirPath,
		fileName: fileName,
	}
}

func NewDefault() *SudoerFile {
	return &SudoerFile{
		dirPath:  defaultSudoersDirPath,
		fileName: defaultSudoersFileName,
	}
}

// adds the given user to the given sudoers file if the user is not there already
func (s *SudoerFile) AddUser(username string) error {
	// first check if the user is in the file
	if sudoers, err := s.parseFile(); err != nil {
		return err
	} else if _, ok := sudoers[username]; !ok {
		// if not, add them
		return s.appendSudoersFile(username)
	}
	// if the user is there, we're all set
	return nil
}

func (s *SudoerFile) sudoFilePath() string {
	if s.dirPath == "" {
		s.dirPath = defaultSudoersDirPath
	}
	if s.fileName == "" {
		s.fileName = defaultSudoersFileName
	}
	return filepath.Join(s.dirPath, s.fileName)
}

// return a set of users (and groups, in theory) in a sudoers file
func (s *SudoerFile) parseFile() (map[string]struct{}, error) {
	result := make(map[string]struct{})

	filePath := s.sudoFilePath()
	fileBytes, err := os.ReadFile(filePath)
	if errors.Is(err, os.ErrNotExist) {
		// if the file doesn't exist, there are no sudoers in it!
		return result, nil
	} else if err != nil {
		return nil, fmt.Errorf("failed to read from sudoers file \"%s\": %s", filePath, err)
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
func (s *SudoerFile) appendSudoersFile(username string) error {
	sudoersEntry := fmt.Sprintf("%s ALL=(ALL) NOPASSWD:ALL\n", username)

	filePath := s.sudoFilePath()
	if file, err := os.OpenFile(filePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, sudoersFilePermissions); err != nil {
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
