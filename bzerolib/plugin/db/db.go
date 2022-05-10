package db

type DbAction string

const (
	Dial DbAction = "dial"
)

type DbActionParams struct {
	SchemaVersion string `json:"schemaVersion"`
	RemotePort    int    `json:"remotePort"`
	RemoteHost    string `json:"remoteHost"`
}
