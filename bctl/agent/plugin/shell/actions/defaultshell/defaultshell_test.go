package defaultshell

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"testing"

	"github.com/stretchr/testify/mock"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"bastionzero.com/bctl/v1/bctl/agent/plugin/shell/actions/defaultshell/pseudoterminal"
	"bastionzero.com/bctl/v1/bzerolib/logger"
	bzshell "bastionzero.com/bctl/v1/bzerolib/plugin/shell"
	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
)

func TestDefaultShell(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Agent Default Shell Suite")
}

var _ = Describe("Default Shell", Ordered, func() {
	runAsUser := "test"
	testContent := "BastionZero"

	logger := logger.DevNullLogger()
	streamMessageChan := make(chan smsg.StreamMessage)
	doneChan := make(chan struct{})

	mockPT := createPseudoTerminal()

	shell := New(logger, streamMessageChan, doneChan, runAsUser)

	Context("Happy Path", func() {

		It("relays messages between the Daemon and a local pseudo terminal", func() {
			By("starting without error")
			action := string(bzshell.ShellOpen)
			actionPayload, _ := json.Marshal(bzshell.ShellOpenMessage{})

			retActionPayload, err := shell.Receive(action, actionPayload)
			Expect(err).To(BeNil())
			Expect(retActionPayload).To(Equal([]byte{}))

			By("passing Daemon input to pseudo terminal")
			action = string(bzshell.ShellInput)
			inputMessage := bzshell.ShellInputMessage{
				Data: []byte(testContent),
			}

			actionPayload, _ = json.Marshal(inputMessage)
			retActionPayload, err = shell.Receive(action, actionPayload)
			Expect(err).To(BeNil())
			Expect(retActionPayload).To(Equal([]byte{}))

			By("relaying previous output to the daemon")
			// check to see if our output is the same as our input
			msg := <-streamMessageChan
			content, err := base64.StdEncoding.DecodeString(string(msg.Content))
			Expect(err).To(BeNil())
			Expect(string(content)).To(Equal(testContent))

			// request shell replay
			action = string(bzshell.ShellReplay)
			actionPayload = []byte{}
			retActionPayload, err = shell.Receive(action, actionPayload)
			Expect(err).To(BeNil())
			Expect(string(retActionPayload)).To(Equal(testContent))

			By("passing Daemon resize to pseudo terminal")
			// create our shell resize payloads
			action = string(bzshell.ShellResize)
			resizeMessage := bzshell.ShellResizeMessage{
				Cols: 2,
				Rows: 3,
			}
			actionPayload, _ = json.Marshal(resizeMessage)

			// send our shell resize message
			retActionPayload, err = shell.Receive(action, actionPayload)
			Expect(err).To(BeNil())
			Expect(retActionPayload).To(Equal([]byte{}))

			By("closing when it's told to")
			action = string(bzshell.ShellClose)
			actionPayload = []byte{}
			retActionPayload, err = shell.Receive(action, actionPayload)
			Expect(err).To(BeNil())
			Expect(retActionPayload).To(Equal(actionPayload))

			<-mockPT.Done()
		})
	})
})

func setMakePseudoTerminal(mockPT pseudoterminal.MockPseudoTerminal) {
	NewPseudoTerminal = func(logger *logger.Logger, runAsUser string, command string) (IPseudoTerminal, error) {
		return mockPT, nil
	}
}

func createPseudoTerminal() pseudoterminal.MockPseudoTerminal {
	mockPT := pseudoterminal.MockPseudoTerminal{}

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
