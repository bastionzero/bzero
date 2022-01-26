package web

import (
	"net/http"
)

type WebAction string

const (
	Dial WebAction = "dial"
)

type WebActionParams struct {
	RemotePort int
	RemoteHost string
}

type WebFood struct {
	Action  WebAction
	Writer  http.ResponseWriter
	Request *http.Request
}
