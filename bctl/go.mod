module bastionzero.com/bctl/v1/bctl

go 1.16

replace bastionzero.com/bctl/v1/bzerolib => ../bzerolib

replace bastionzero.com/bctl/v1/bctl => ./

require (
	bastionzero.com/bctl/v1/bzerolib v0.0.0
	github.com/Masterminds/semver v1.5.0
	github.com/creack/pty v1.1.17
	github.com/fsnotify/fsnotify v1.5.1
	github.com/google/uuid v1.2.0
	github.com/gorilla/websocket v1.4.2
	github.com/stretchr/testify v1.7.0
	golang.org/x/build v0.0.0-20211108163316-3ce30f35b9aa
	golang.org/x/sys v0.0.0-20210630005230-0f9fa26af87c
	golang.org/x/term v0.0.0-20210220032956-6a3ed077a48d
	gopkg.in/tomb.v2 v2.0.0-20161208151619-d5d1b5820637
	k8s.io/api v0.21.3
	k8s.io/apimachinery v0.21.3
	k8s.io/client-go v0.21.3
)
