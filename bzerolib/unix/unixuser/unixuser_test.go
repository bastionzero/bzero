package unixuser

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestUnixUser(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Unix Suite: UnixUser, Permission, Create")
}

var _ = Describe("Unix", Ordered, func() {
	bzeroDaddyPath := filepath.Join(os.TempDir(), "bzero")
	if err := os.Mkdir(bzeroDaddyPath, 0700); err != nil { // create dir
		panic(err)
	}

	Context("UnixUser", func() {
		fakeUser := "fakeyfake"
		currentUser, err := user.Current()
		if err != nil {
			panic(err)
		}

		It("Looks up users based on their username", func() {
			By("finding legitimate users")
			_, err := Lookup(currentUser.Username)
			Expect(err).To(BeNil())

			By("not finding illegitimate users")
			_, err = Lookup(fakeUser)
			Expect(err).ToNot(BeNil())
		})

		It("Finds the current user", func() {
			usr, err := Current()
			Expect(err).To(BeNil())
			Expect(usr.Username).To(Equal(currentUser.Username))
		})

		It("Finds all suplementary groups IDs for a given user", func() {
			usr, err := Current()
			Expect(err).To(BeNil())

			currentGids, err := currentUser.GroupIds()
			Expect(err).To(BeNil())

			userGids, err := usr.GroupIds()
			Expect(err).To(BeNil())
			Expect(len(currentGids)).To(Equal(len(userGids)))
		})
	})

	Context("Permissions", func() {
		usr, _ := Current()
		fmt.Printf("running test as: %s uid: %d\n", usr.Name, usr.Uid)

		// create a directory in temp with 700 permission and owned by this user
		ourPath := filepath.Join(bzeroDaddyPath, "ourdir")
		if err := os.Mkdir(ourPath, 0700); err != nil { // create dir
			panic(err)
		} else if err := os.Chown(ourPath, int(usr.Uid), int(usr.Gid)); err != nil { // change owner of dir to us
			panic(err)
		}

		// create a directory in temp with 000 permission
		theirPath := filepath.Join(bzeroDaddyPath, "theirdir")
		if err := os.Mkdir(theirPath, 0000); err != nil { // create dir that no one has permissions to
			panic(err)
		}

		It("Allows the creation of directories on behalf of the user", func() {
			By("making a directory in a folder we have permissions for")
			err := usr.Mkdir(filepath.Join(ourPath, "ourSubDir"), 0700)
			Expect(err).To(BeNil())

			By("rejected to make a directory in a folder we don't have permissions for")
			err = usr.Mkdir(filepath.Join(theirPath, "theirSubDir"), 0700)
			var permissionError PermissionDeniedError
			Expect(errors.As(err, &permissionError)).To(BeTrue())
		})

		It("Calls OpenFile on behalf of the user", func() {
			perms := fs.FileMode(0300)

			By("creating a file in a directory we have permissions for")
			ourFilePath := filepath.Join(ourPath, "ourFile")
			_, err := usr.OpenFile(ourFilePath, os.O_CREATE|os.O_WRONLY, perms)
			Expect(err).To(BeNil())

			By("not creating a file in a directory we don't have permissions for")
			theirFilePath := filepath.Join(theirPath, "theirFile")
			_, err = usr.OpenFile(theirFilePath, os.O_CREATE|os.O_WRONLY, perms)
			Expect(err).ToNot(BeNil())

			By("opening a file we have the correct permissions for")
			_, err = usr.OpenFile(ourFilePath, os.O_WRONLY, perms)
			Expect(err).To(BeNil())

			By("not opening a file we don't have the permissions for")
			_, err = usr.OpenFile(ourFilePath, os.O_RDWR, perms)
			Expect(err).ToNot(BeNil())
		})
	})

	Context("Create User", func() {
		It("creates a new user", func() {
			validCommand := regexp.MustCompile(`^\S+useradd -m \S+(( --[a-z]+ \S+)*)$`)
			runCommand = func(cmd *exec.Cmd) error {
				fmt.Printf("\n Generated command: %s\n", cmd.String())
				Expect(validCommand.Match([]byte(cmd.String()))).To(BeTrue())
				return nil
			}
			validateUserCreation = func(username string) (*UnixUser, error) {
				return &UnixUser{}, nil
			}

			By("not creating a user it isn't allowed to")
			_, err := LookupOrCreateFromList("sneakyman")
			Expect(err).ToNot(BeNil())

			By("creating a user it is allowed to")
			_, err = LookupOrCreateFromList("ssm-user")
			Expect(err).To(BeNil())

			By("adding a normal user with the specified options")
			opts := UserAddOptions{
				ExpireDate: time.Now().Add(24 * time.Hour),
			}
			_, err = Create("bastion-zero", opts)
			Expect(err).To(BeNil())

			By("creating a sudoer user with specified options")
			sudoerUserName := "bzero-test"
			opts.Sudoer = true
			opts.SudoersFolderName = bzeroDaddyPath
			_, err = Create(sudoerUserName, opts)
			Expect(err).To(BeNil())

			// check that our sudoers line was added correctly
			fileBytes, err := os.ReadFile(filepath.Join(bzeroDaddyPath, defaultSudoersFileName))
			Expect(err).To(BeNil())

			expectedSudoerEntry := fmt.Sprintf("%s ALL=(ALL) NOPASSWD:ALL", sudoerUserName)
			Expect(strings.Contains(string(fileBytes), expectedSudoerEntry)).To(BeTrue())
		})
	})

	AfterAll(func() {
		os.RemoveAll(bzeroDaddyPath)
	})
})
