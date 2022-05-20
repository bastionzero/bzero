package authorizedkeys

import (
	"fmt"
	"math/rand"
	"os"
	"time"
)

/*
The authorized keys file contains all of the keys for a given user that they are allowed to authenticate as to ssh. When
our ssh plugin is interacting with local ssh, this can require constantly reading and writing to the file which introduces
a lof of riskiness and the potential to do so in a conflicting or unsafe manner.

We cannot use a traditional lock because there is no easy way (with our current architecture) to give all instances of the ssh
plugin access to a single mutex, so this is implemented using and environment variable. The environment variable is unique
to each user and is considered "free" in its unset state.

env variable name: BZERO_$USERNAME_AUTHORIZED_FILE_LOCK

This will allow us to only have this variable set during short intervals. If a customer is connecting to a single box as 10
different users, then an environment dump won't result in 10 wierd new variables.
new variables.

This lock is imperfect. Sometimes, if many requests are attempting to write or read from the file at the same time, it is
possible that one or more instances will read from the variable at exactly the same time resulting in locks being granted
where it shouldn't have. This is a limitation of the implementation and is acceptable.
*/

const (
	busy    = "busy"
	timeout = 5 * time.Second
)

type authorizedFileLock struct {
	User string
}

func (a *authorizedFileLock) Get() {
	lock := a.authorizedFileLockEnv()

	// check if the lock is free (aka unset) and if so, grab it
	if os.Getenv(lock) == "" {
		os.Setenv(lock, busy)
		return
	}

	// we need to wait until the lock is available
	// a random int is chosen between 1-15 milliseconds to minimize collision
	interval := time.Duration(rand.Intn(14) + 1) // interval can't be zero
	checkEnvVariable := time.NewTicker(interval * time.Millisecond)
	absoluteTimeout := time.NewTicker(timeout)
	for {
		select {
		case <-checkEnvVariable.C:
			if os.Getenv(lock) == "" {
				os.Setenv(lock, busy)
				return
			}
		case <-absoluteTimeout.C:
			return
		}
	}
}

func (a *authorizedFileLock) Release() {
	os.Unsetenv(a.authorizedFileLockEnv())
}

func (a *authorizedFileLock) authorizedFileLockEnv() string {
	return fmt.Sprintf("BZERO_%s_AUTHORIZED_FILE_LOCK", a.User)
}
