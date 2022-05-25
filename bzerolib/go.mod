module bastionzero.com/bctl/v1/bzerolib

go 1.16

replace bastionzero.com/bctl/v1/bctl => ../bctl

replace bastionzero.com/bctl/v1/bzerolib => ./

require (
	bastionzero.com/bctl/v1/bctl v0.0.0-00010101000000-000000000000
	github.com/cenkalti/backoff/v4 v4.1.1
	github.com/coreos/go-oidc/v3 v3.0.0
	github.com/fsnotify/fsnotify v1.5.1 // indirect
	github.com/gofrs/flock v0.8.1 // indirect
	github.com/gorilla/websocket v1.4.2
	github.com/onsi/gomega v1.19.0
	github.com/rs/zerolog v1.24.0
	github.com/stretchr/testify v1.7.0
	golang.org/x/crypto v0.0.0-20210921155107-089bfa567519
	gopkg.in/natefinch/lumberjack.v2 v2.0.0
	gopkg.in/tomb.v2 v2.0.0-20161208151619-d5d1b5820637
	k8s.io/apimachinery v0.21.3
)
