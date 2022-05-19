package tcpservice

import (
	"net"

	"github.com/stretchr/testify/mock"
)

// mocked version of the TcpService
type MockTcpService struct {
	TcpService
	mock.Mock
}

func (m MockTcpService) ResolveTCPAddr(network string, address string) (*net.TCPAddr, error) {
	args := m.Called(network, address)
	return args.Get(0).(*net.TCPAddr), args.Error(1)
}

func (m MockTcpService) DialTCP(network string, laddr *net.TCPAddr, raddr *net.TCPAddr) (*net.TCPConn, error) {
	args := m.Called(network, laddr, raddr)
	return args.Get(0).(*net.TCPConn), args.Error(1)
}
