package unixuser

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	sudoersFolder          = "/etc/sudoers.d"
	defaultSudoersFileName = "bastionzero-users"
	sudoersFilePermissions = 0640
)

const (
	addUserCommand   = "useradd"
	expireTimeFormat = "2006-01-02"

	// option flags
	homeDirFlag    = "--home"
	expireDateFlag = "--expiredate"
	gidFlag        = "--gid"
	groupsFlag     = "--groups"
	shellFlag      = "--shell"
)

type UserAddOptions struct {
	Sudoer          bool
	SudoersFileName string
	HomeDir         string
	ExpireDate      time.Time
	Gid             uint32
	Groups          []uint32
	Shell           string
}

func Create(username string, options UserAddOptions) error {
	if ok := validateUsername(username); !ok {
		return fmt.Errorf("invalid username: %s", username)
	}

	// build our command line args
	args := []string{username}
	homePath := filepath.Clean(strings.TrimSpace(options.HomeDir))
	if filepath.IsAbs(homePath) {
		args = append(args, homeDirFlag, homePath)
	}

	if options.ExpireDate.After(time.Now()) {
		args = append(args, expireDateFlag, options.ExpireDate.Format(expireTimeFormat))
	}

	if options.Gid != 0 {
		args = append(args, gidFlag, fmt.Sprint(options.Gid))
	}

	if len(options.Groups) > 0 {
		gidsList := strings.Trim(strings.Replace(fmt.Sprint(options.Groups), " ", ",", -1), "[]")
		args = append(args, groupsFlag, gidsList)
	}

	shellPath := filepath.Clean(strings.TrimSpace(options.Shell))
	if filepath.IsAbs(shellPath) {
		args = append(args, shellFlag, shellPath)
	}

	// run the command to add a new user
	cmd := exec.Command(addUserCommand, args...)
	var exitError *exec.ExitError
	if err := cmd.Run(); errors.As(err, &exitError) {
		stderr := strings.ToLower(string(exitError.Stderr))
		if strings.Contains(stderr, "permission denied") {
			return PermissionDeniedError(fmt.Sprintf("failed to create user %s: %s", username, stderr))
		}
	} else if err != nil {
		return err
	}

	if options.Sudoer {
		// determine our sudoers sudoersFile name
		sudoersFile := strings.TrimSpace(options.SudoersFileName)
		if sudoersFile == "" {
			sudoersFile = defaultSudoersFileName
		}

		if err := addToSudoers(username, sudoersFile); err != nil {
			return err
		}
	}

	return nil
}

func addToSudoers(username string, sudoersFile string) error {
	sudoersFilePath := filepath.Join(sudoersFolder, sudoersFile)
	sudoersEntry := fmt.Sprintf("%s ALL=(ALL) NOPASSWD:ALL\n", username)

	// open the file as the current user so that we can bubble up any permissions errors
	if usr, err := Current(); err != nil {
		return fmt.Errorf("failed to determine current user")
	} else if file, err := usr.OpenFile(sudoersFilePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, sudoersFilePermissions); err != nil {
		return err
	} else {
		// if the file was previously empty, then we make sure to add a comment
		if fi, err := file.Stat(); err == nil && fi.Size() == 0 {
			sudoersFileComment := fmt.Sprintf("# Created by the BastionZero Agent on %s\n\n", time.Now().Round(time.Second))
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
