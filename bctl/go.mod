module bastionzero.com/bctl/v1/bctl

go 1.17

replace bastionzero.com/bctl/v1/bzerolib => ../bzerolib

replace bastionzero.com/bctl/v1/bctl => ./

require (
	bastionzero.com/bctl/v1/bzerolib v0.0.0
	github.com/Masterminds/semver v1.5.0
	github.com/creack/pty v1.1.17
	github.com/fsnotify/fsnotify v1.5.1
	github.com/google/uuid v1.2.0
	github.com/gorilla/websocket v1.4.2
	github.com/onsi/ginkgo/v2 v2.1.4
	github.com/onsi/gomega v1.19.0
	github.com/stretchr/testify v1.7.0
	github.com/wk8/go-ordered-map v0.2.0
	golang.org/x/build v0.0.0-20211108163316-3ce30f35b9aa
	golang.org/x/sys v0.0.0-20220319134239-a9b59b0215f8
	golang.org/x/term v0.0.0-20210927222741-03fcf44c2211
	gopkg.in/tomb.v2 v2.0.0-20161208151619-d5d1b5820637
	k8s.io/api v0.21.3
	k8s.io/apimachinery v0.21.3
	k8s.io/client-go v0.21.3
)

require (
	github.com/cenkalti/backoff/v4 v4.1.1 // indirect
	github.com/coreos/go-oidc/v3 v3.0.0 // indirect
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/go-logr/logr v0.4.0 // indirect
	github.com/gogo/protobuf v1.3.2 // indirect
	github.com/golang/protobuf v1.5.2 // indirect
	github.com/google/go-cmp v0.5.6 // indirect
	github.com/google/gofuzz v1.1.0 // indirect
	github.com/googleapis/gnostic v0.4.1 // indirect
	github.com/json-iterator/go v1.1.10 // indirect
	github.com/moby/spdystream v0.2.0 // indirect
	github.com/modern-go/concurrent v0.0.0-20180306012644-bacd9c7ef1dd // indirect
	github.com/modern-go/reflect2 v1.0.1 // indirect
	github.com/pkg/errors v0.9.1 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	github.com/rs/zerolog v1.24.0 // indirect
	github.com/stretchr/objx v0.2.0 // indirect
	golang.org/x/crypto v0.0.0-20210921155107-089bfa567519 // indirect
	golang.org/x/net v0.0.0-20220225172249-27dd8689420f // indirect
	golang.org/x/oauth2 v0.0.0-20210628180205-a41e5a781914 // indirect
	golang.org/x/text v0.3.7 // indirect
	golang.org/x/time v0.0.0-20210723032227-1f47c861a9ac // indirect
	google.golang.org/appengine v1.6.7 // indirect
	google.golang.org/protobuf v1.27.1 // indirect
	gopkg.in/inf.v0 v0.9.1 // indirect
	gopkg.in/natefinch/lumberjack.v2 v2.0.0 // indirect
	gopkg.in/square/go-jose.v2 v2.5.1 // indirect
	gopkg.in/yaml.v2 v2.4.0 // indirect
	gopkg.in/yaml.v3 v3.0.0-20200313102051-9f266ea9e77c // indirect
	k8s.io/klog/v2 v2.8.0 // indirect
	k8s.io/utils v0.0.0-20201110183641-67b214c5f920 // indirect
	sigs.k8s.io/structured-merge-diff/v4 v4.1.2 // indirect
	sigs.k8s.io/yaml v1.2.0 // indirect
)
