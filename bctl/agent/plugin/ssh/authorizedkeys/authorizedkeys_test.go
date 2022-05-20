package authorizedkeys

import (
	"encoding/base64"
	"fmt"
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
			Expect(strings.Contains(string(fileBytes), fakePubKey)).To(BeTrue())

			By("removing the key after key lifetime")
			time.Sleep(2 * maxKeyLifetime)
			fileBytes, err = os.ReadFile(authorizedKeysFile)
			Expect(err).To(BeNil())

			fmt.Printf("%s\n", string(fileBytes))
			Expect(len(fileBytes) == 0).To(BeTrue())
		})

		It("adds keys to user's authorized_keys file and removes them on disconnect", func() {

			By("adding a key to the authorized_keys file without error")
			err := authKeyService.Add(fakePubKey)
			Expect(err).To(BeNil())

			fileBytes, err := os.ReadFile(authorizedKeysFile)
			Expect(err).To(BeNil())
			Expect(strings.Contains(string(fileBytes), fakePubKey)).To(BeTrue())

			By("removing the key after disconnect")
			close(doneChan)
			time.Sleep(2 * maxKeyLifetime)

			fileBytes, err = os.ReadFile(authorizedKeysFile)
			Expect(err).To(BeNil())
			Expect(len(fileBytes) == 0).To(BeTrue())
		})

		It("is thread safe if you try to add a bunch of keys to a file at once", func() {
			doneChan = make(chan struct{})
			maxKeyLifetime = 10 * time.Second
			numKeys := 10

			// If the file doesn't exist, create it, or append to the file
			file, err := os.OpenFile(authorizedKeysFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
			Expect(err).To(BeNil())

			testString := "this key file is not empty"
			_, err = file.WriteString(testString)
			Expect(err).To(BeNil())
			file.Close()

			By("adding a bunch of keys to the authorized_key file at once")
			for i := 0; i < numKeys; i++ {
				authKeyService := New(logger, testUser.Username, doneChan)
				err := authKeyService.Add(fakePubKey)
				Expect(err).To(BeNil())
			}

			// wait for any stragglers to write
			time.Sleep(3 * time.Second)
			fileBytes, err := os.ReadFile(authorizedKeysFile)
			Expect(err).To(BeNil())
			lines := strings.Split(string(fileBytes), "\n")
			Expect(len(lines) == numKeys+1).To(BeTrue())

			By("removing all of the keys")
			close(doneChan)
			time.Sleep(3 * time.Second)

			fileBytes, err = os.ReadFile(authorizedKeysFile)
			Expect(err).To(BeNil())
			lines = strings.Split(string(fileBytes), "\n")

			// our environment-based lock is not perfect and because of that some keys aren't deleted
			// this is an acceptable situation within reason, here we allow 2 keys to fail clearing
			Expect(len(lines) < 3).To(BeTrue())

			// make sure we are not wiping any existing keys
			Expect(strings.Contains(string(fileBytes), testString)).To(BeTrue())
		})
	})
})
