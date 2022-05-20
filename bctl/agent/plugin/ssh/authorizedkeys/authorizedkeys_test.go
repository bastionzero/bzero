package authorizedkeys

import (
	"encoding/base64"
	"os"
	"os/user"
	"path"
	"strings"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"bastionzero.com/bctl/v1/bzerolib/logger"
)

func TestDefaultSsh(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Agent Authorized Keys Suite")
}

var _ = Describe("Agent Add Authorized Keys", func() {
	authorizedKeyFileName = "fake_authorized_keys"
	maxKeyLifetime = 1 * time.Second

	logger := logger.MockLogger()
	testUser, _ := user.Current()
	doneChan := make(chan struct{})

	authorizedKeysFile := path.Join(testUser.HomeDir, ".ssh", authorizedKeyFileName)

	fakePubKey := "ssh-rsa " + base64.StdEncoding.EncodeToString([]byte("fake"))

	Context("Happy Path", func() {
		AfterEach(func() {
			os.Remove(authorizedKeysFile)
		})

		authKeyService := New(logger, testUser.Username, doneChan)

		It("adds keys to user's authorized_keys file and removes them after expiration", func() {

			By("adding a key to the authorized_keys file without error")
			err := authKeyService.Add(fakePubKey)
			Expect(err).To(BeNil())

			fileBytes, err := os.ReadFile(authorizedKeysFile)
			Expect(err).To(BeNil())
			Expect(len(fileBytes) > 0).To(BeTrue())
			Expect(strings.Contains(string(fileBytes), fakePubKey)).To(BeTrue())

			By("removing the key after key lifetime")
			time.Sleep(2 * maxKeyLifetime)
			fileBytes, err = os.ReadFile(authorizedKeysFile)
			Expect(err).To(BeNil())
			Expect(len(fileBytes) == 0).To(BeTrue())
		})
	})

	Context("Early Disconnect Path", func() {
		AfterEach(func() {
			os.Remove(authorizedKeysFile)
		})

		authKeyService := New(logger, testUser.Username, doneChan)

		It("adds keys to user's authorized_keys file and removes them on disconnect", func() {

			By("adding a key to the authorized_keys file without error")
			err := authKeyService.Add(fakePubKey)
			Expect(err).To(BeNil())

			fileBytes, err := os.ReadFile(authorizedKeysFile)
			Expect(err).To(BeNil())
			Expect(len(fileBytes) > 0).To(BeTrue())

			By("removing the key after disconnect")
			close(doneChan)
			time.Sleep(2 * maxKeyLifetime)

			fileBytes, err = os.ReadFile(authorizedKeysFile)
			Expect(err).To(BeNil())
			Expect(len(fileBytes) == 0).To(BeTrue())
		})
	})
})
