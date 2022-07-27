package servers

import (
	"fmt"

	"bastionzero.com/bctl/v1/bctl/daemon/datachannel"
	"bastionzero.com/bctl/v1/bzerolib/channels/websocket"
	"gopkg.in/tomb.v2"
)

func ComeUpWithCoolName(daemonShutdownChan chan struct{}, doneChan chan error, websocket *websocket.Websocket, dc *datachannel.DataChannel, dcTmb *tomb.Tomb) {
	for {
		select {
		case _, ok := <-daemonShutdownChan:
			if !ok {
				// TODO: not entirely certain this is right
				err := fmt.Errorf("daemon was shut down")
				websocket.Close(err)
				dc.Close(err)
				// TODO: break?
			}
		// FIXME: how do we wait for both the websocket and datachannel...
		// websocket.Close() has a tmb.Wait(), that's good
		// does datachannel.Close() need one?
		case <-dcTmb.Dead():
			doneChan <- dcTmb.Err()
			break
		}
	}
}
