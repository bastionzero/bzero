package lockservice

import (
	"fmt"
	"os"

	"github.com/gofrs/flock"
)

// we need to implement a small client layer on top of flock to make sure we manage
// the proper deletion of the lock file it creates
type LockService interface {
	TryLock() (bool, error)
	Unlock() error
}

type FlockLockService struct {
	LockService
	lock     *flock.Flock
	lockFile string
}

func New(path string) *FlockLockService {
	return &FlockLockService{
		lock:     flock.New(path),
		lockFile: path,
	}
}

func (f *FlockLockService) TryLock() (bool, error) {
	return f.lock.TryLock()
}

func (f *FlockLockService) Unlock() error {
	if err := f.lock.Unlock(); err != nil {
		return fmt.Errorf("Failed to release lock: %s", err)
	} else if err := os.Remove(f.lockFile); err != nil {
		return fmt.Errorf("Failed to remove lock file: %s", err)
	}

	return nil
}
