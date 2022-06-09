package pseudoterminal

import (
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"bastionzero.com/bctl/v1/bzerolib/logger"
	"bastionzero.com/bctl/v1/bzerolib/unix/unixuser"
)

func TestPseudoTerminal(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Pseudo Terminal Suite")
}

var _ = Describe("Pseudo Terminal", Ordered, func() {
	Context("Happy Path", func() {
		commandstr := ""
		logger := logger.MockLogger()
		var terminal *PseudoTerminal

		// create our pseudo terminal
		BeforeEach(func() {
			usr, err := unixuser.Current()
			Expect(err).To(BeNil())
			terminal, err = New(logger, usr, commandstr)
			Expect(err).To(BeNil())
		})

		It("creates a new pseudo terminal", func() {
			Expect(terminal).ToNot(BeNil())
			Expect(terminal.command).ToNot(BeNil())
			Expect(terminal.ptyFile).ToNot(BeNil())
			Expect(terminal.StdIn()).ToNot(BeNil())
			Expect(terminal.StdOut()).ToNot(BeNil())
		})

		It("runs commands", func() {
			// we use a command that requires calculation so that we don't confuse an error that
			// outputs the entire string with a successful execution
			keystrokes := "expr 1 + 1\n"
			expectedOutput := "2"

			for _, char := range keystrokes {
				_, err := terminal.StdIn().Write([]byte(string(char)))
				Expect(err).To(BeNil())
			}
			time.Sleep(1 * time.Second) // let the command run

			stdoutBytes := make([]byte, 1000)
			n, err := terminal.StdOut().Read(stdoutBytes)
			Expect(err).To(BeNil())

			Expect(string(stdoutBytes[:n])).To(ContainSubstring(expectedOutput))
		})

		It("shuts down properly", func() {
			for {
				go func() {
					time.Sleep(1 * time.Second)
					terminal.Kill()
				}()

				select {
				case <-terminal.Done():
					return
				case <-time.After(5 * time.Second):
					panic("terminal failed to die")
				}
			}
		})

		It("sets terminal size", func() {
			err := terminal.SetSize(10, 10)
			Expect(err).To(BeNil())
		})
	})
})
