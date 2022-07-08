package bzcert

import (
	"encoding/base64"

	"bastionzero.com/bctl/v1/bzerolib/keysplitting/util"
	"github.com/stretchr/testify/mock"
)

// mocked version of the FileService
type MockBZCert struct {
	BZCert
	mock.Mock
}

func (m MockBZCert) Verify(idpProvider string, idpOrgId string) error {
	args := m.Called()
	return args.Error(0)
}

func (m MockBZCert) Refresh() error {
	args := m.Called()
	return args.Error(0)
}

func (m MockBZCert) Hash() string {
	args := m.Called()
	cert := args.Get(0).(BZCert)
	hashBytes, _ := util.HashPayload(cert)
	return base64.StdEncoding.EncodeToString(hashBytes)
}

func (m MockBZCert) PrivateKey() string {
	args := m.Called()
	return args.String(0)
}

func (m MockBZCert) Expired() bool {
	args := m.Called()
	return args.Bool(0)
}

func (m MockBZCert) Cert() *BZCert {
	args := m.Called()
	return args.Get(0).(*BZCert)
}
