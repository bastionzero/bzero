package filelock

import (
	"fmt"
	"os"

	"github.com/gofrs/flock"
)

/* we need to implement a small client layer on top of flock to make sure we manage
the proper deletion of the lock file it creates, and so that parent processes can
require that all of their children use the same underlying lock file

The exact usage pattern of FileLock will depend somewhat on who needs the lock -- for example,
see the docstring of Cleanup()
*/
type FileLock struct {
	lockFile string
}

// create a new FlockLockService using `path` as the underlying lockfile
func NewFileLock(path string) *FileLock {
	return &FileLock{
		lockFile: path,
	}
}

// get a new lock -- you can use its `TryLock` and `Unlock` methods to ensure threadsafe file operation
func (f *FileLock) NewLock() (*flock.Flock, error) {
	if f.lockFile == "" {
		return nil, fmt.Errorf("you must use the NewFileLock() constructor and set a file path first")
	}
	return flock.New(f.lockFile), nil
}

// remove the underlying lock file from the OS
// if a parent process gives locks to its children, it should call Cleanup() when all children are done
// if a process or class creates its own lock, it should not call Cleanup(), since its siblings may be looking at the same file
func (f *FileLock) Cleanup() error {
	if err := os.Remove(f.lockFile); err != nil {
		return fmt.Errorf("failed to remove lock file: %s", err)
	}

	return nil
}
