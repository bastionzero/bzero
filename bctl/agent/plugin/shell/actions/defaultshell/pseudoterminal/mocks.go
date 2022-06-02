package pseudoterminal

import (
	"io"

	"github.com/stretchr/testify/mock"
)

type MockPseudoTerminal struct {
	mock.Mock
	PseudoTerminal
}

func (m MockPseudoTerminal) StdIn() io.Writer {
	args := m.Called() // <- empty because StdIn takes no args
	return args.Get(0).(io.Writer)
}

func (m MockPseudoTerminal) StdOut() io.Reader {
	args := m.Called()
	return args.Get(0).(io.Reader)
}

func (m MockPseudoTerminal) SetSize(cols, rows uint32) error {
	args := m.Called(cols, rows)
	return args.Error(0)
}

func (m MockPseudoTerminal) Done() <-chan struct{} {
	args := m.Called()
	return args.Get(0).(chan struct{})
}

func (m MockPseudoTerminal) Kill() {
	m.Called()
}
