package defaultshell

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"testing"

	"bastionzero.com/bctl/v1/bzerolib/logger"
	bzshell "bastionzero.com/bctl/v1/bzerolib/plugin/shell"
	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"gopkg.in/tomb.v2"
)

var runAsUser = "Bojji"

type MockPseudoTerminal struct {
	mock.Mock
	IPseudoTerminal
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

func setMakePseudoTerminal(mockPT MockPseudoTerminal) {
	NewPseudoTerminal = func(logger *logger.Logger, runAsUser string, command string) (IPseudoTerminal, error) {
		return mockPT, nil
	}
}

func createPseudoTerminal() MockPseudoTerminal {
	mockPT := MockPseudoTerminal{}

	// pipe takes anything that's written to the writer and outputs it via the reader
	// writer.Write("genius") -> reader.Read() => "genius"
	reader, writer := io.Pipe()
	mockPT.On("StdIn").Return(writer)
	mockPT.On("StdOut").Return(reader)

	mockPT.On("SetSize", uint32(2), uint32(3)).Return(nil)

	doneChan := make(chan struct{})
	mockPT.On("Done").Return(doneChan)

	mockPT.On("Kill").Return().Run(func(args mock.Arguments) {
		close(doneChan)
	})

	setMakePseudoTerminal(mockPT)
	return mockPT
}

func shellOpen(t *testing.T, dshell *DefaultShell) {
	action := string(bzshell.ShellOpen)
	actionPayload := []byte{}
	retAction, retActionPayload, err := dshell.Receive(action, actionPayload)
	assert.Nil(t, err)
	assert.Equal(t, retAction, action)
	assert.Equal(t, retActionPayload, actionPayload)
	assert.NotNil(t, dshell.terminal)
}

func shellInput(t *testing.T, dshell *DefaultShell, testContent string) {
	action := string(bzshell.ShellInput)
	inputMessage := bzshell.ShellInputMessage{
		Data: []byte(testContent),
	}
	actionPayload, err := json.Marshal(inputMessage)
	assert.Nil(t, err)

	retAction, retActionPayload, err := dshell.Receive(action, actionPayload)
	assert.Nil(t, err)
	assert.Equal(t, retAction, action)
	assert.Equal(t, retActionPayload, []byte{})
}

func TestShellOpen(t *testing.T) {
	createPseudoTerminal()

	streamMessageChan := make(chan smsg.StreamMessage)
	dshell, err := New(&tomb.Tomb{}, logger.MockLogger(), streamMessageChan, runAsUser)
	assert.Nil(t, err)

	shellOpen(t, dshell)
}

func TestShellClose(t *testing.T) {
	mockPT := createPseudoTerminal()

	streamMessageChan := make(chan smsg.StreamMessage)
	dshell, err := New(&tomb.Tomb{}, logger.MockLogger(), streamMessageChan, runAsUser)
	assert.Nil(t, err)

	shellOpen(t, dshell)

	action := string(bzshell.ShellClose)
	actionPayload := []byte{}
	retAction, retActionPayload, err := dshell.Receive(action, actionPayload)
	assert.Nil(t, err)
	assert.Equal(t, retAction, action)
	assert.Equal(t, retActionPayload, actionPayload)

	<-mockPT.Done()
}

func TestShellInput(t *testing.T) {
	createPseudoTerminal()

	streamMessageChan := make(chan smsg.StreamMessage)
	dshell, err := New(&tomb.Tomb{}, logger.MockLogger(), streamMessageChan, runAsUser)
	assert.Nil(t, err)

	shellOpen(t, dshell)

	testContent := "BastionZero"
	shellInput(t, dshell, testContent)

	// check to see if our output is the same as our input
	msg := <-streamMessageChan
	content, err := base64.StdEncoding.DecodeString(string(msg.Content))
	assert.Nil(t, err)
	assert.Equal(t, testContent, string(content))
}

func TestShellResize(t *testing.T) {
	createPseudoTerminal()

	streamMessageChan := make(chan smsg.StreamMessage)
	dshell, err := New(&tomb.Tomb{}, logger.MockLogger(), streamMessageChan, runAsUser)
	assert.Nil(t, err)

	shellOpen(t, dshell)

	// create our shell resize payloads
	action := string(bzshell.ShellResize)
	inputMessage := bzshell.ShellResizeMessage{
		Cols: 2,
		Rows: 3,
	}
	actionPayload, err := json.Marshal(inputMessage)
	assert.Nil(t, err)

	// send our shell resize message
	retAction, retActionPayload, err := dshell.Receive(action, actionPayload)
	assert.Nil(t, err)
	assert.Equal(t, retAction, action)
	assert.Equal(t, retActionPayload, []byte{})
}

func TestShellReplay(t *testing.T) {
	createPseudoTerminal()

	// init shell
	streamMessageChan := make(chan smsg.StreamMessage)
	dshell, err := New(&tomb.Tomb{}, logger.MockLogger(), streamMessageChan, runAsUser)
	assert.Nil(t, err)

	shellOpen(t, dshell)

	testContent := "BastionZero"
	shellInput(t, dshell, testContent)

	// check to see if our output is the same as our input
	msg := <-streamMessageChan
	content, err := base64.StdEncoding.DecodeString(string(msg.Content))
	assert.Nil(t, err)
	assert.Equal(t, testContent, string(content))

	// request shell replay
	action := string(bzshell.ShellReplay)
	actionPayload := []byte{}
	retAction, retActionPayload, err := dshell.Receive(action, actionPayload)
	assert.Nil(t, err)
	assert.Equal(t, retAction, action)
	assert.Equal(t, string(retActionPayload), testContent)
}
