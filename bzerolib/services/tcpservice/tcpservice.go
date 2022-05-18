package tcpservice

import "net"

type TcpService interface {
	ResolveTCPAddr(network string, address string) (*net.TCPAddr, error)
	DialTCP(network string, laddr *net.TCPAddr, raddr *net.TCPAddr) (*net.TCPConn, error)
}

type NetTcpService struct {
	TcpService
}

func (t NetTcpService) ResolveTCPAddr(network string, address string) (*net.TCPAddr, error) {
	return net.ResolveTCPAddr(network, address)
}

func (t NetTcpService) DialTCP(network string, laddr *net.TCPAddr, raddr *net.TCPAddr) (*net.TCPConn, error) {
	return net.DialTCP(network, laddr, raddr)
}
