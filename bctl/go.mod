module bastionzero.com/bctl/v1/bctl

go 1.16

replace bastionzero.com/bctl/v1/bzerolib => ../bzerolib

replace bastionzero.com/bctl/v1/bctl => ./

require (
	bastionzero.com/bctl/v1/bzerolib v0.0.0
	github.com/google/uuid v1.2.0
	golang.org/x/build v0.0.0-20211108163316-3ce30f35b9aa
	gopkg.in/tomb.v2 v2.0.0-20161208151619-d5d1b5820637
	k8s.io/api v0.21.3
	k8s.io/apimachinery v0.21.3
	k8s.io/client-go v0.21.3
)
