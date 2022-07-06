package authorizedkeys

import (
	"encoding/base64"
	"os"
	"path"
	"strings"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"bastionzero.com/bctl/v1/bzerolib/logger"
	"bastionzero.com/bctl/v1/bzerolib/unix/unixuser"
)

func TestAuthorizedKeys(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Agent Authorized Keys Suite")
}

var _ = Describe("Agent Authorized Keys", Ordered, func() {
	authorizedKeyFolder := "fake_ssh"
	logger := logger.MockLogger()
	testUser, _ := unixuser.Current()

	authorizedKeysFile := path.Join(testUser.HomeDir, authorizedKeyFolder, authorizedKeyFileName)
	os.OpenFile(authorizedKeysFile, os.O_RDONLY|os.O_CREATE, 0666)

	fakePubKey := "ssh-rsa " + base64.StdEncoding.EncodeToString([]byte("fake"))

	AfterEach(func() {
		os.RemoveAll(path.Join(testUser.HomeDir, authorizedKeyFolder))
	})

	Context("Happy Path", func() {

		doneChan := make(chan struct{})

		It("adds keys to user's authorized_keys file and removes them after expiration", func() {
			authKeyService, _ := New(logger, doneChan, testUser, authorizedKeyFolder, authorizedKeyFolder, time.Second)

			By("adding a key to the authorized_keys file without error")
			err := authKeyService.Add(fakePubKey)
			Expect(err).To(BeNil())

			fileBytes, err := os.ReadFile(authorizedKeysFile)
			Expect(err).To(BeNil())
			Expect(fileBytes).To(ContainSubstring(string(fakePubKey)))

			By("removing the key after key lifetime")
			time.Sleep(3 * time.Second)
			fileBytes, err = os.ReadFile(authorizedKeysFile)
			Expect(err).To(BeNil())
			Expect(len(fileBytes)).To(Equal(0))
		})

		It("adds keys to user's authorized_keys file and removes them on disconnect", func() {
			authKeyService, _ := New(logger, doneChan, testUser, authorizedKeyFolder, authorizedKeyFolder, time.Second)

			By("adding a key to the authorized_keys file without error")
			err := authKeyService.Add(fakePubKey)
			Expect(err).To(BeNil())

			fileBytes, err := os.ReadFile(authorizedKeysFile)
			Expect(err).To(BeNil())
			Expect(fileBytes).To(ContainSubstring(string(fakePubKey)))

			By("removing the key after disconnect")
			close(doneChan)
			time.Sleep(2 * time.Second)

			fileBytes, err = os.ReadFile(authorizedKeysFile)
			Expect(err).To(BeNil())
			Expect(len(fileBytes)).To(Equal(0))
		})
	})

	Context("Stressful Path", func() {
		doneChan := make(chan struct{})
		numKeys := 10

		It("allows for writing many keys at once", func() {
			time.Sleep(time.Second)

			// If the file doesn't exist, create it, or append to the file
			err := os.MkdirAll(path.Join(testUser.HomeDir, authorizedKeyFolder), os.ModePerm)
			Expect(err).To(BeNil())
			file, err := os.OpenFile(authorizedKeysFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0666)
			Expect(err).To(BeNil())

			testString := "this key file is not empty"
			_, err = file.WriteString(testString)
			Expect(err).To(BeNil())
			file.Close()

			By("adding a bunch of keys to the authorized_key file at once")

			for i := 0; i < numKeys; i++ {
				go func() {
					authKeyService, err := New(logger, doneChan, testUser, authorizedKeyFolder, authorizedKeyFolder, 30*time.Second)
					Expect(err).To(BeNil())
					err = authKeyService.Add(fakePubKey)
					Expect(err).To(BeNil())
				}()
			}

			// wait for any stragglers to write
			time.Sleep(time.Second)
			fileBytes, err := os.ReadFile(authorizedKeysFile)
			Expect(err).To(BeNil())
			lines := strings.Split(string(fileBytes), "\n")
			Expect(len(lines)).To(Equal(numKeys + 1))

			By("removing all of the keys by closing")
			close(doneChan)
			time.Sleep(5 * time.Second)

			fileBytes, err = os.ReadFile(authorizedKeysFile)
			Expect(err).To(BeNil())
			lines = strings.Split(string(fileBytes), "\n")

			// this should equal 2 -- 1 for the non-deleted key and 1 newline
			Expect(len(lines)).To(Equal(2))

			// make sure we are not wiping any existing keys
			Expect(strings.Contains(string(fileBytes), testString)).To(BeTrue())
		})
	})
})
