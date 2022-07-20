package exec

import (
	"fmt"

	"github.com/stretchr/testify/mock"
	"k8s.io/client-go/tools/remotecommand"
)

type MockExecutor struct {
	mock.Mock
	remotecommand.Executor
}

func (m MockExecutor) Stream(options remotecommand.StreamOptions) error {
	// if there is no stdin, this will be empty
	var data = make([]byte, 7)
	go func() {
		for {
			if options.Stdin != nil {
				options.Stdin.Read(data)
			}
			options.Stdout.Write(data)
			options.Stderr.Write([]byte(fmt.Sprintf("error: %s", data)))
		}
	}()

	// NOTE: using Called() with the entire options object is infeasible
	// because the action creates some of its own pointers
	// it's enough to check that stdwriter has the right members
	args := m.Called(options.Stdout)
	return args.Error(0)
}
