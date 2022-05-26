package authorizedkeys

import (
	"encoding/base64"
	"io/ioutil"
	"os"
	"os/user"
	"path"
	"strings"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"bastionzero.com/bctl/v1/bzerolib/logger"
	"bastionzero.com/bctl/v1/bzerolib/services/lockservice"
)

func TestDefaultSsh(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Agent Authorized Keys Suite")
}

var _ = Describe("Agent Authorized Keys", func() {
	authorizedKeyFolder, _ = ioutil.TempDir("", "fake_ssh")
	authorizedKeyFileName = "fake_authorized_keys"
	logger := logger.MockLogger()
	testUser, _ := user.Current()

	authorizedKeysFile := path.Join(testUser.HomeDir, authorizedKeyFolder, authorizedKeyFileName)
	os.OpenFile(authorizedKeysFile, os.O_RDONLY|os.O_CREATE, 0666)
	lockFile := path.Join(testUser.HomeDir, authorizedKeyFolder, "test-lock.lock")
	lockService := lockservice.NewLockService(lockFile)
	defer lockService.Cleanup()

	fakePubKey := "ssh-rsa " + base64.StdEncoding.EncodeToString([]byte("fake"))

	AfterEach(func() {
		os.RemoveAll(path.Join(testUser.HomeDir, authorizedKeyFolder))
	})

	Context("Happy Path", func() {

		doneChan := make(chan struct{})

		authKeyService := New(logger, testUser.Username, doneChan, lockService, time.Second)

		It("adds keys to user's authorized_keys file and removes them after expiration", func() {

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
					authKeyService := New(logger, testUser.Username, doneChan, lockService, 30*time.Second)
					err := authKeyService.Add(fakePubKey)
					Expect(err).To(BeNil())
				}()
			}

			// wait for any stragglers to write
			time.Sleep(time.Second)
			fileBytes, err := os.ReadFile(authorizedKeysFile)
			Expect(err).To(BeNil())
			lines := strings.Split(string(fileBytes), "\n")
			Expect(len(lines)).To(Equal(numKeys + 1))

			By("removing all of the keys")
			close(doneChan)
			time.Sleep(3 * time.Second)

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
