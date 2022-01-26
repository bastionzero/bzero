module bastionzero.com/bctl/v1/bctl

go 1.16

replace bastionzero.com/bctl/v1/bzerolib => ../bzerolib

replace bastionzero.com/bctl/v1/bctl => ./

require (
	bastionzero.com/bctl/v1/bzerolib v0.0.0
	github.com/google/uuid v1.2.0
	github.com/gorilla/websocket v1.4.2 // indirect
	github.com/ovh/configstore v0.5.2 // indirect
	github.com/tucnak/store v0.0.0-20170905113834-b02ecdcc6dfb // indirect
	golang.org/x/build v0.0.0-20211108163316-3ce30f35b9aa
	golang.org/x/net v0.0.0-20210726213435-c6fcb2dbf985 // indirect
	gopkg.in/tomb.v2 v2.0.0-20161208151619-d5d1b5820637
	k8s.io/api v0.21.3
	k8s.io/apimachinery v0.21.3
	k8s.io/client-go v0.21.3
)
