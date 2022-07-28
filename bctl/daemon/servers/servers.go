package servers

import (
	"fmt"

	"gopkg.in/tomb.v2"
)

type IServer interface {
	Shutdown(err error)
	DaemonShutdownChan() chan struct{}
}

type IEphemeralServer interface {
	IServer
	DataChannelTomb() *tomb.Tomb
	DoneChan() chan error // TODO: make write-only
}

func CoolServerFunc(server IServer) {
	if _, ok := <-server.DaemonShutdownChan(); !ok {
		server.Shutdown(fmt.Errorf("daemon was shut down"))
	}
}

func CoolEphemeralServerFunc(server IEphemeralServer) {
	dcTmb := server.DataChannelTomb()
	for {
		select {
		case _, ok := <-server.DaemonShutdownChan():
			if !ok {
				server.Shutdown(fmt.Errorf("daemon was shut down"))
			}
		case <-dcTmb.Dead():
			server.DoneChan() <- dcTmb.Err()
			return
		}
	}
}
