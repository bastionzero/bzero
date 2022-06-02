/*
This package extends golang's user package, to be specifically for unix and specifically for the use cases
Bastion Zero has.

It does 3 main things:
1. Extends the golang user package but ensuring that uid and gid(s) are all ints
	- Lookup(username string) (*UnixUser, error)
	- Current() (*UnixUser, error)
	- (u *UnixUser) GroupIds() ([]int, error)
2. Allows permission checking against file paths. This group of functions returns whether permission is granted
and, if not, why.
    - (u *UnixUser) CanRead(path string) (bool, error)
	- (u *UnixUser) CanWrite(path string) (bool, error)
	- (u *UnixUser) CanExecute(path string) (bool, error)
	- (u *UnixUser) CanOpen(path string) (bool, error)
	- (u *UnixUser) CanCreate(path string) (bool, error)
3. Allows for file operations as a user. These functions will check permissions before acting and if there was
any creation, they will set the owner of the created file to the user
    - (u *UnixUser) Mkdir(path string, perm fs.FileMode) error
	- (u *UnixUser) OpenFile(path string, flag int, perm fs.FileMode) (*os.File, error)
*/
package unixuser

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/user"
	"regexp"
	"strconv"
	"strings"
)

const (
	passwdFile = "/etc/passwd"
)

type UnixUser struct {
	Uid      uint32
	Gid      uint32
	Username string
	Name     string
	HomeDir  string
	Shell    string
	usr      *user.User
}

func Lookup(username string) (*UnixUser, error) {
	if err := validateUsername(username); err != nil {
		return nil, err
	} else if usr, err := user.Lookup(username); err != nil {
		return nil, err
	} else {
		return convertToUnixUser(usr)
	}
}

func Current() (*UnixUser, error) {
	if usr, err := user.Current(); err != nil {
		return nil, err
	} else {
		return convertToUnixUser(usr)
	}
}

func (u *UnixUser) GroupIds() ([]uint32, error) {
	if gids, err := u.usr.GroupIds(); err != nil {
		return nil, err
	} else {
		gidInts := []uint32{}
		for _, gid := range gids {
			if gidInt, err := strconv.Atoi(gid); err != nil {
				return gidInts, fmt.Errorf("failed to convert string GID %s to int: %s", gid, err)
			} else {
				gidInts = append(gidInts, uint32(gidInt))
			}
		}
		return gidInts, nil
	}
}

// this function creates a directory as a user. First it checks user permissions against
// the directory path and then creates the directory with the user as the owner
func (u *UnixUser) Mkdir(path string, perm fs.FileMode) error {
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		if ok, err := u.CanCreate(path); !ok { // check permissions to create
			return fmt.Errorf("user %s cannot create %s: %s", u.Username, path, err)
		} else if err := os.Mkdir(path, perm); err != nil { // create dir
			return fmt.Errorf("failed to create %s: %s", path, err)
		} else if err := os.Chown(path, int(u.Uid), int(u.Gid)); err != nil { // change owner of dir to user
			return fmt.Errorf("failed to set user %s as directory %s owner", u.Username, path)
		}
	} else if err != nil {
		return fmt.Errorf("failed to check whether path %s exists: %s", path, err)
	}

	return nil
}

// this function opens or creates a file as a user. It follows the same pattern as the os's
// os.OpenFile(name string, flag int, perm fs.FileMode) (*os.File, error)
// but does permissions checks before the file can be read or created and makes sure that
// the user is set as the owner of the file
func (u *UnixUser) OpenFile(path string, flag int, perm fs.FileMode) (*os.File, error) {
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) && flag&os.O_CREATE != 0 {

		// if the file doesn't exist, then we create it
		if ok, err := u.CanCreate(path); !ok { // check permissions to create file
			return nil, fmt.Errorf("user %s cannot create %s: %s", u.Username, path, err)
		} else if err := os.WriteFile(path, []byte{}, perm); err != nil { // create file
			return nil, fmt.Errorf("failed to create %s: %s", path, err)
		} else if err := os.Chown(path, int(u.Uid), int(u.Gid)); err != nil { // change owner of file to user
			return nil, fmt.Errorf("failed to set user %s as owner of file %s: %s", u.Username, path, err)
		}
	} else if err != nil {
		return nil, fmt.Errorf("failed to check whether path %s exists: %s", path, err)
	}

	// when opening a file, users specify at least one of the following flags: O_RDONLY,
	// O_WRONLY, O_RDWR which we check against the user's permissions
	switch {
	case flag&os.O_RDONLY != 0:
		if ok, err := u.CanRead(path); !ok {
			return nil, fmt.Errorf("user %s cannot read %s: %s", u.Username, path, err)
		}
	case flag&os.O_WRONLY != 0:
		if ok, err := u.CanWrite(path); !ok {
			return nil, fmt.Errorf("user %s cannot write to %s: %s", u.Username, path, err)
		}
	case flag&os.O_RDWR != 0:
		if ok, err := u.CanRead(path); !ok {
			return nil, fmt.Errorf("user %s cannot read %s: %s", u.Username, path, err)
		} else if ok, err := u.CanWrite(path); !ok {
			return nil, fmt.Errorf("user %s cannot write to %s: %s", u.Username, path, err)
		}
	}

	return os.OpenFile(path, flag, perm)
}

// Get the default shell for the user based on configuration in /etc/passwd file.
func getDefaultShell(usrName string) string {
	// read the passwd file file
	fileBytes, err := os.ReadFile(passwdFile)
	if err != nil {
		return ""
	}

	// iterate through file lines until we find the entry for our user
	scanner := bufio.NewScanner(bytes.NewReader(fileBytes))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, usrName) {
			// passwd file entries come in the form of
			// username:password:uid:gid:GECOS(user id info):home:shell
			entries := strings.Split(line, ":")
			return strings.TrimSpace(entries[len(entries)-1])
		}
	}

	return ""
}

// test that the provided username is valid unix user name
// source: https://unix.stackexchange.com/a/435120
func validateUsername(username string) error {
	usernamePattern := "^[a-z_]([a-z0-9_-]{0,31}|[a-z0-9_-]{0,30}\\$)$"
	var usernameMatch, _ = regexp.MatchString(usernamePattern, username)
	if !usernameMatch {
		return fmt.Errorf("invalid username provided: %s", username)
	}
	return nil
}

func convertToUnixUser(usr *user.User) (*UnixUser, error) {
	if uid, err := strconv.Atoi(usr.Uid); err != nil {
		return nil, fmt.Errorf("failed to convert user string UID to int: %s", err)
	} else if gid, err := strconv.Atoi(usr.Gid); err != nil {
		return nil, fmt.Errorf("failed to convert user string GID to int: %s", err)
	} else if err := validateUsername(usr.Username); err != nil {
		return nil, err
	} else {
		return &UnixUser{
			Uid:      uint32(uid),
			Gid:      uint32(gid),
			Username: usr.Username,
			Name:     usr.Name,
			HomeDir:  usr.HomeDir,
			Shell:    getDefaultShell(usr.Username),
			usr:      usr,
		}, nil
	}
}
