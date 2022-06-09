module bastionzero.com/bctl/v1/bctl

go 1.16

replace bastionzero.com/bctl/v1/bzerolib => ../bzerolib

replace bastionzero.com/bctl/v1/bctl => ./

require (
	bastionzero.com/bctl/v1/bzerolib v0.0.0
	github.com/Masterminds/semver v1.5.0
	github.com/creack/pty v1.1.18
	github.com/fsnotify/fsnotify v1.5.1
	github.com/google/uuid v1.2.0
	github.com/gorilla/websocket v1.4.2
	github.com/onsi/ginkgo/v2 v2.1.4
	github.com/onsi/gomega v1.19.0
	github.com/stretchr/testify v1.7.0
	github.com/wk8/go-ordered-map v0.2.0
	golang.org/x/build v0.0.0-20211108163316-3ce30f35b9aa
	golang.org/x/crypto v0.0.0-20210921155107-089bfa567519
	golang.org/x/sys v0.0.0-20220520151302-bc2c85ada10a
	golang.org/x/term v0.0.0-20210927222741-03fcf44c2211
	gopkg.in/tomb.v2 v2.0.0-20161208151619-d5d1b5820637
	k8s.io/api v0.21.3
	k8s.io/apimachinery v0.21.3
	k8s.io/client-go v0.21.3
)
