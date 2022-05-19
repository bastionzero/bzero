package userservice

import "os/user"

const ValidUsernameRegex = "^[a-z_]([a-z0-9_-]{0,31}|[a-z0-9_-]{0,30}\\$)$"

// a lightweight interface around User methods and constants
type UserService interface {
	Lookup(username string) (*user.User, error)
}

// the default implementation
type OsUserService struct {
	UserService
}

func (u OsUserService) Lookup(username string) (*user.User, error) {
	return user.Lookup(username)
}
