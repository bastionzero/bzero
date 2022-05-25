package lockservice

import (
	"fmt"
	"os"

	"github.com/gofrs/flock"
)

// we need to implement a small client layer on top of flock to make sure we manage
// the proper deletion of the lock file it creates, and so that parent processes can
// require that all of their children use the same underlying lock file
type LockService interface {
	NewLock() (*flock.Flock, error)
	Cleanup() error
}

type FlockLockService struct {
	LockService
	lockFile string
}

// create a new FlockLockService using `path` as the underlying lockfile
func NewLockService(path string) *FlockLockService {
	return &FlockLockService{
		lockFile: path,
	}
}

// get a new lock -- you can use its `TryLock` and `Unlock` methods to ensure threadsafe file operation
func (f *FlockLockService) NewLock() (*flock.Flock, error) {
	if f.lockFile == "" {
		return nil, fmt.Errorf("You must use the NewLockService() constructor and set a file path first")
	}
	return flock.New(f.lockFile), nil
}

// you should always call `defer Cleanup()` on your LockService to ensure the underlying lockfile is removed
func (f *FlockLockService) Cleanup() error {
	if err := os.Remove(f.lockFile); err != nil {
		return fmt.Errorf("Failed to remove lock file: %s", err)
	}

	return nil
}
