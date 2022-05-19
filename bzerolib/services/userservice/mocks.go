package userservice

import (
	"os/user"

	"github.com/stretchr/testify/mock"
)

// mocked version of the UserService
type MockUserService struct {
	UserService
	mock.Mock
}

func (m MockUserService) Lookup(username string) (*user.User, error) {
	args := m.Called(username)
	return args.Get(0).(*user.User), args.Error(1)
}
