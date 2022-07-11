package unixuser

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"time"
)

// These are the users we're allowed to create, if someone is trying to look them up
var allowedToCreate = UserList{
	"ssm-user":   {Sudoer: true},
	"bzero-user": {Sudoer: true},
}

// these variable functions are overwriten by tests
var runCommand = func(cmd *exec.Cmd) error {
	return cmd.Run()
}

var validateUserCreation = func(username string) (*UnixUser, error) {
	return Lookup(username)
}

const (
	addUserCommand   = "useradd"
	expireTimeFormat = "2006-01-02"

	// option flags
	homeDirFlag    = "--home"
	expireDateFlag = "--expiredate"
	gidFlag        = "--gid"
	groupsFlag     = "--groups"
	shellFlag      = "--shell"

	// sudoers constants
	defaultSudoersFolderName = "/etc/sudoers.d"
	defaultSudoersFileName   = "bastionzero-users"
	sudoersFilePermissions   = 0640
)

// key'ed by user name
type UserList map[string]UserAddOptions

type UserAddOptions struct {
	HomeDir    string
	ExpireDate time.Time
	Gid        uint32
	Groups     []uint32
	Shell      string

	// sudoer config variables
	Sudoer            bool   // defaults to false
	SudoersFolderName string // defaults to const defaultSudoersFolderName
	SudoersFileName   string // defaults to const defaultSudoersFileName
}

// TODO: instead of using hardcoded list, accept UserList arg so that ssh and shell could have differently configured lists
// This function will lookup users from a list or create them if they don't exist
func LookupOrCreateFromList(username string) (*UnixUser, error) {
	// check that user doesn't exist before we try to create it
	var unknownUser user.UnknownUserError
	fmt.Printf("Something's happening here and what it is is %s\n", username)
	if usr, err := Lookup(username); errors.As(err, &unknownUser) {
		fmt.Printf("Sorry %s -- we couldn't find you!\n", username)
		if opts, ok := allowedToCreate[username]; !ok {
			return nil, fmt.Errorf("%s does not exist", username)
		} else if err := userAdd(username, opts); err != nil {
			return nil, err
		} else {
			// make sure we really did create the user
			return validateUserCreation(username)
		}
	} else if err != nil {
		fmt.Printf("Sorry %s -- something bad happened: %s!\n", username, err)
		return nil, err
	} else {
		fmt.Printf("Congrats %s -- we did find you!\n", username)
		return usr, nil
	}
}

func Create(username string, options UserAddOptions) (*UnixUser, error) {
	var unknownUser user.UnknownUserError
	if usr, err := Lookup(username); errors.As(err, &unknownUser) {
		if err := userAdd(username, options); err != nil {
			return nil, err
		} else {
			// make sure we really did create the user
			return validateUserCreation(username)
		}
	} else if err != nil {
		return nil, err
	} else {
		return usr, nil
	}
}

func userAdd(username string, options UserAddOptions) error {
	// build our command line args
	args := []string{"-m", username}
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
	if err := runCommand(cmd); errors.As(err, &exitError) {
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

		// determine our sudoers folder name
		sudoersFolder := strings.TrimSpace(options.SudoersFolderName)
		if sudoersFile == "" {
			sudoersFolder = defaultSudoersFolderName
		}

		// add our user to the sudoers file
		if err := addToSudoers(username, filepath.Join(sudoersFolder, sudoersFile)); err != nil {
			return err
		}
	}

	return nil
}

func addToSudoers(username string, sudoersFilePath string) error {
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
