package unixuser

import (
	"errors"
	"fmt"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"time"
)

// These are the users we're allowed to create, if someone is trying to look them up
var managedUsers = UserList{
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
	if usr, err := Lookup(username); errors.As(err, &unknownUser) {
		if opts, ok := managedUsers[username]; !ok {
			return nil, fmt.Errorf("%s does not exist", username)
		} else if err := userAdd(username, opts); err != nil {
			return nil, err
		} else {
			// make sure we really did create the user
			return validateUserCreation(username)
		}
	} else if err != nil {
		return nil, err
	} else {
		// TODO: (CWC-1982) long-term we shouldn't need this behavior, but it acts as a failsafe
		// for users whose sudoers files are broken
		// if this is a managed user, make sure it's in sudoers if it should be
		if opts, ok := managedUsers[username]; ok {
			addUserToSudoers(username, opts)
		}
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

	return addUserToSudoers(username, options)
}

// adds a user to the provided (or default) sudoers file, provided they are a sudoer
func addUserToSudoers(username string, options UserAddOptions) error {
	if options.Sudoer {
		// determine our sudoers sudoersFile name
		sudoersFile := strings.TrimSpace(options.SudoersFileName)
		if sudoersFile == "" {
			sudoersFile = defaultSudoersFileName
		}

		// determine our sudoers folder name
		sudoersFolder := strings.TrimSpace(options.SudoersFolderName)
		if sudoersFolder == "" {
			sudoersFolder = defaultSudoersFolderName
		}

		// add our user to the sudoers file
		if err := addToSudoers(username, filepath.Join(sudoersFolder, sudoersFile)); err != nil {
			return err
		}
	}
	return nil
}
