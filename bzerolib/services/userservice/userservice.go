package userservice

import "os/user"

// a lightweight interface around User methods
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
