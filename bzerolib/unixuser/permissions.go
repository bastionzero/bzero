/*
This file covers the functions that check whether a user has a certain given file permissions
based on the file path.

Possible permissions checks:
    - read: whether the user can read a file
    - write: whether the user can write to a file
    - execute: whether the user can execute a file

The following two permissions types are abstractions that I've found helpful. They're not explicitly
stated (the only permissions we ever get defined for a given user group are the previous 3), but
they might still be interesting for the code to check and understand especially if the code writer
doesn't want to go read a whole bunch on unix file permissions.
    - open: whether the user can open a file (aka execute)
    - create: whether the user can create the given file. Does this check by traveling through the
	given path and the checking for write + execute permissions on the lowest subdir that exists in the
	path.

The permissions verfication code mimics the permission checking process undertaken by the unix kernel
and as dictated by Advanced Programming in the Unix Environment Third Edition by W. Richard Stevens
and Stephen A. Rago (p. 101).

Permission validation process:
1. if user's uid is 0 (aka "root"), then it can do whatever it wants
2. if user's uid is the same as the owner of the file, check for access perms, else reject access
3. if any of the user's gids matches the gid of the file, check for access perms, else reject access
4. check if any other user is allowed to do what we want, else reject

These steps are taken in sequence, if you're the owner and you don't have the right permissions, we don't
fall back onto group logic, etc. There are no second chances in unix.

The code seeks help from the modeParser object to abstract away some of the more annoying bit checking logic
*/
package unixuser

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

type checkPermissionMode string

const (
	read    checkPermissionMode = "read"
	write   checkPermissionMode = "write"
	execute checkPermissionMode = "execute"
	open    checkPermissionMode = "open"
	create  checkPermissionMode = "create"
)

type userGroup string

const (
	owner userGroup = "owner"
	group userGroup = "group"
	other userGroup = "other"
)

func (u *UnixUser) CanRead(path string) (bool, error) {
	return u.checkPermissions(path, read)
}

func (u *UnixUser) CanWrite(path string) (bool, error) {
	return u.checkPermissions(path, write)
}

func (u *UnixUser) CanExecute(path string) (bool, error) {
	return u.checkPermissions(path, execute)
}

func (u *UnixUser) CanOpen(path string) (bool, error) {
	return u.checkPermissions(path, open)
}

func (u *UnixUser) CanCreate(path string) (bool, error) {
	if path == "/" {
		return u.checkPermissions(path, create)
	} else {
		path = strings.TrimSuffix(path, "/")
	}

	// loop through all dirs in path until it finds one that exists
	// and it can check permissions of.
	// filepath.Dir will return "." if the filepath is empty
	for path != "." && !strings.Contains(path, "./") {
		if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
			path = filepath.Dir(path)
			continue
		} else if err != nil {
			return false, fmt.Errorf("failed to read file path: %s", err)
		} else {
			return u.checkPermissions(path, create)
		}
	}
	return u.checkPermissions(path, create)
}

func (u *UnixUser) checkPermissions(path string, mode checkPermissionMode) (bool, error) {
	info, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, fmt.Errorf("path does not exist: %s", path)
	} else if err != nil {
		return false, fmt.Errorf("error grabbing file %s info: %s", path, err)
	} else if info.Sys() == nil {
		return false, fmt.Errorf("unable to retrieve owner or group")
	}

	// create our parser so we can abstract annoying bit checks
	perms := newFileModeParser(info.Mode())

	// check if user is root or the file owner
	fileUid := info.Sys().(*syscall.Stat_t).Uid
	switch uint32(u.Uid) {
	case 0:
		// if you're root, you can do anything
		return true, nil
	case fileUid:
		if ok := perms.verify(owner, mode); !ok {
			return false, fmt.Errorf("user is owner but does not have correct permissions: %s", info.Mode().String())
		} else {
			return true, nil
		}
	}

	// check to see if file belongs to any group that the user is in
	fileGid := info.Sys().(*syscall.Stat_t).Gid
	if gids, err := u.GroupIds(); err != nil {
		return false, fmt.Errorf("failed to get user groups: %s", err)
	} else {
		for _, gid := range gids {
			if uint32(gid) == fileGid {
				if ok := perms.verify(group, mode); !ok {
					return false, fmt.Errorf("user is a group member but does not have correct permissions: %s", info.Mode().String())
				} else {
					return true, nil
				}
			}
		}
	}

	// check to see if anyone can write to the file
	if ok := perms.verify(other, mode); !ok {
		return false, fmt.Errorf("user is neither owner nor group member but still does not have correct permissions: %s", info.Mode().String())
	} else {
		return true, nil
	}
}
