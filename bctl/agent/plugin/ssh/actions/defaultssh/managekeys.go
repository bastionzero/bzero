package defaultssh

import (
	"fmt"
	"os/exec"
)

const shellPluginCommandName = "sh"

var shellPluginCommandArgs = []string{"-c"}

// Appends an authorized key entry to the authorized_keys file within username's .ssh directory
func addToAuthorizedKeyFile(username string, authorizedKey string) (bool, error) {
	// make a .ssh directory for the user if it doesnt exist and then append the authorizedKey string to the authorized_keys file within the .ssh directory
	authorizedKeyFile := fmt.Sprintf("~%s/.ssh/authorized_keys", username)
	sshFolder := fmt.Sprintf("~%s/.ssh", username)
	createSshDirectory := fmt.Sprintf("mkdir -p %s", sshFolder)
	shellCmdArgsCreateSshDirectory := append(shellPluginCommandArgs, createSshDirectory)
	cmdCreateSshDirectory := exec.Command(shellPluginCommandName, shellCmdArgsCreateSshDirectory...)
	if err := cmdCreateSshDirectory.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			// The program has exited with an exit code != 0
			return false, fmt.Errorf("error executing %s %v", cmdCreateSshDirectory.Args, exitErr.Error())
		}
		return false, nil
	}

	appendKeyCmd := fmt.Sprintf("echo '%s' >> %s", authorizedKey, authorizedKeyFile)
	shellCmdArgsAppendKey := append(shellPluginCommandArgs, lockFolderAndRunCommand(authorizedKeyFile, appendKeyCmd))
	cmdAppendKey := exec.Command(shellPluginCommandName, shellCmdArgsAppendKey...)
	if err := cmdAppendKey.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			// The program has exited with an exit code != 0
			return false, fmt.Errorf("error executing %s %v", cmdAppendKey.Args, exitErr.Error())
		}
		return false, nil
	}
	return true, nil
}

// Use flock to wait for exclusive lock on fileToLock for up to 10 seconds (or exit) and then run a command
func lockFolderAndRunCommand(folderToLock string, commandToRun string) string {
	return fmt.Sprintf("flock -x -w 10 %s -c \"%s\"", folderToLock, commandToRun)
}
