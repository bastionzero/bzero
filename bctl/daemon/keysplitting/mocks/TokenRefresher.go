// Code generated by mockery v2.12.2. DO NOT EDIT.

package mocks

import (
	testing "testing"

	mock "github.com/stretchr/testify/mock"

	tokenrefresh "bastionzero.com/bctl/v1/bctl/daemon/keysplitting/tokenrefresh"
)

// TokenRefresher is an autogenerated mock type for the TokenRefresher type
type TokenRefresher struct {
	mock.Mock
}

// Refresh provides a mock function with given fields:
func (_m *TokenRefresher) Refresh() (*tokenrefresh.ZLIKeysplittingConfig, error) {
	ret := _m.Called()

	var r0 *tokenrefresh.ZLIKeysplittingConfig
	if rf, ok := ret.Get(0).(func() *tokenrefresh.ZLIKeysplittingConfig); ok {
		r0 = rf()
	} else {
		if ret.Get(0) != nil {
			r0 = ret.Get(0).(*tokenrefresh.ZLIKeysplittingConfig)
		}
	}

	var r1 error
	if rf, ok := ret.Get(1).(func() error); ok {
		r1 = rf()
	} else {
		r1 = ret.Error(1)
	}

	return r0, r1
}

// NewTokenRefresher creates a new instance of TokenRefresher. It also registers the testing.TB interface on the mock and a cleanup function to assert the mocks expectations.
func NewTokenRefresher(t testing.TB) *TokenRefresher {
	mock := &TokenRefresher{}
	mock.Mock.Test(t)

	t.Cleanup(func() { mock.AssertExpectations(t) })

	return mock
}