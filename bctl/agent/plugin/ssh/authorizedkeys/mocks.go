package authorizedkeys

import "github.com/stretchr/testify/mock"

// mocked version of the FileService
type MockAuthorizedKey struct {
	AuthorizedKeys
	mock.Mock
}

func (m MockAuthorizedKey) Add(pubkey string) error {
	args := m.Called()
	return args.Error(0)
}
