package authorizedkeys

import (
	"fmt"
	"os"
	"os/user"
	"strconv"
	"syscall"
)

func checkPermissionToCreate(path string, usr *user.User) (bool, error) {
	return checkPermissions(path, usr, true)
}

func checkPermissionToWrite(path string, usr *user.User) (bool, error) {
	return checkPermissions(path, usr, false)
}

// This function checks to see whether a user has permissions to create a file in a directory. It
// mimics the permission checking process undertaken by the kernel and as dictated by Advanced
// Programming in the Unix Environment Third Edition by W. Richard Stevens and Stephen A. Rago
// (p. 101).
//
// In order to make sure we can create files in a directory, we need to check that the user has both
// write and execute permissions.
//
// Permission validation process:
// 1. if user's uid is 0 (aka "root"), then it can do whatever it wants
// 2. if user's uid is the same as the owner of the file, check for access perms, else reject access
// 3. if any of the user's gids matches the gid of the file, check for access perms, else reject access
// 4. check if any other user is allowed to do what we want, else reject
//
// These steps are taken in sequence, if you're the owner and you don't have the right permissions, we don't
// fall back onto group logic, etc.
//
// FileInfo mode comes in format "drwxrwxrwx" always:
// https://cs.opensource.google/go/go/+/refs/tags/go1.18.2:src/io/fs/fs.go;drc=2580d0e08d5e9f979b943758d3c49877fb2324cb;bpv=1;bpt=1;l=194?gsn=String&gs=kythe%3A%2F%2Fgo.googlesource.com%2Fgo%3Flang%3Dgo%3Fpath%3Dio%2Ffs%23method%2520FileMode.String
func checkPermissions(path string, usr *user.User, checkExecute bool) (bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		return false, fmt.Errorf("path does not exist")
	} else if info.Sys() == nil {
		return false, fmt.Errorf("unable to retrieve owner or group")
	}

	// check if user is root or the file owner
	fileUID := info.Sys().(*syscall.Stat_t).Uid
	if uid, err := strconv.Atoi(usr.Uid); err != nil {
		return false, fmt.Errorf("failed to convert user string GID to int: %s", err)
	} else if uid == 0 { // if you're root, you can do anything
		return true, nil
	} else if uint32(uid) == fileUID { // if file owner is user
		// check that owner has write and execute permissions
		ownerWrite := string(info.Mode().String()[2]) == "w"
		ownerExecute := string(info.Mode().String()[3]) == "x"
		if ownerWrite && ownerExecute || ownerWrite && !checkExecute {
			return true, nil
		} else {
			// no second chances
			return false, fmt.Errorf("user is owner but does not have correct permissions: %s", info.Mode().String())
		}
	}

	// check to see if file belongs to any group that the user is in
	fileGID := info.Sys().(*syscall.Stat_t).Gid
	if gids, err := usr.GroupIds(); err != nil {
		return false, fmt.Errorf("failed to get user groups: %s", err)
	} else {
		for _, gid := range gids {
			if gidInt, err := strconv.Atoi(gid); err != nil {
				return false, fmt.Errorf("failed to convert user string GID to int: %s", err)
			} else if uint32(gidInt) == fileGID {
				// check if group has write and execute permissions
				groupWrite := string(info.Mode().String()[5]) == "w"
				groupExecute := string(info.Mode().String()[6]) == "x"
				if groupWrite && groupExecute || groupWrite && !checkExecute {
					return true, nil
				} else {
					// no second chances
					return false, fmt.Errorf("user is a group memeber but does not have correct permissions: %s", info.Mode().String())
				}
			}
		}
	}

	// check to see if anyone can write to the file
	othersWrite := string(info.Mode().String()[8]) == "w"
	othersExecute := string(info.Mode().String()[9]) == "x"
	if othersWrite && othersExecute || othersWrite && !checkExecute {
		return true, nil
	} else {
		return false, fmt.Errorf("user is neither owner nor group member but still does not have correct permissions: %s", info.Mode().String())
	}
}
