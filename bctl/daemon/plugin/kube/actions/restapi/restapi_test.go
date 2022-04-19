package restapi

import (
	"flag"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMain(m *testing.M) {
	flag.Parse()
	//oldMakeRequest := makeRequest
	//defer func() { makeRequest = oldMakeRequest }()

	exitCode := m.Run()

	// Exit
	os.Exit(exitCode)
}

func TestRestApi(t *testing.T) {
	assert := assert.New(t)
	// request id, log id, command being run

}
