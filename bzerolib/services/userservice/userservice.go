package userservice

import "os/user"

type UserService interface {
	Lookup(username string) (*user.User, error)
}

type OsUserService struct {
	UserService
}

func (u OsUserService) Lookup(username string) (*user.User, error) {
	return user.Lookup(username)
}
