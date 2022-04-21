// Copyright 2018 Amazon.com, Inc. or its affiliates. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License"). You may not
// use this file except in compliance with the License. A copy of the
// License is located at
//
// http://aws.amazon.com/apache2.0/
//
// or in the "license" file accompanying this file. This file is distributed
// on an "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND,
// either express or implied. See the License for the specific language governing
// permissions and limitations under the License.

// This code has been modified from the code covered by the Apache License 2.0.
// Modifications Copyright (C) 2022 BastionZero Inc.  The BastionZero Agent
// is licensed under the Apache 2.0 License.

package pseudoterminal

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path"
	"strconv"
	"strings"
	"syscall"

	"bastionzero.com/bctl/v1/bzerolib/logger"
	"github.com/creack/pty"
)

const (
	termEnvVariable     = "TERM=xterm-256color"
	langEnvVariable     = "LANG=C.UTF-8"
	langEnvVariableKey  = "LANG"
	homeEnvVariable     = "HOME=/home/"
	groupsIdentifier    = "groups="
	defaultShellCommand = "sh"
)

type PseudoTerminal struct {
	logger  *logger.Logger
	ptyFile *os.File
	command *exec.Cmd
}

// StartPty starts pty and provides handles to stdin and stdout
func New(logger *logger.Logger, runAsUser string, commandstr string) (*PseudoTerminal, error) {
	logger.Info("Starting up pseudo terminal")

	// Check if runAsUser is valid and exists
	if strings.TrimSpace(runAsUser) == "" {
		return nil, errors.New("no RunAsUser provided")
	}

	// Attempt to get default shell to use for the runAsUser
	logger.Debugf("Trying to get default shell to use for user %s", runAsUser)
	shellCommand, err := getDefaultShellForUser(runAsUser)
	if err != nil {
		logger.Errorf("Failed getting default shell for user %s: %s", runAsUser, err)
	}

	shellCommandArgs := []string{"-c"}
	if err := doesUserExist(runAsUser, shellCommand, shellCommandArgs); err != nil {
		// if user does not exist, fail the session
		return nil, fmt.Errorf("failed to determine whether %s user exists: %s", runAsUser, err)
	}

	logger.Debugf("Using default shell %s", shellCommand)

	if cmd, err := buildCommand(commandstr, shellCommand, shellCommandArgs, runAsUser); err != nil {
		return nil, err
	} else if ptyFile, err := pty.Start(cmd); err != nil {
		logger.Errorf("Failed to start pty: %s\n", err)
		return nil, fmt.Errorf("failed to start pty: %s", err)
	} else {
		return &PseudoTerminal{
			logger:  logger,
			ptyFile: ptyFile,
			command: cmd,
		}, nil
	}
}

func buildCommand(commandstr string, shellCommand string, shellCommandArgs []string, runAsUser string) (*exec.Cmd, error) {
	var cmd *exec.Cmd

	if strings.TrimSpace(commandstr) == "" {
		cmd = exec.Command(shellCommand)
	} else {
		commandArgs := append(shellCommandArgs, commandstr)
		cmd = exec.Command(shellCommand, commandArgs...)
	}

	// TERM is set as linux by pty which has an issue where vi editor screen does not get cleared.
	// Setting TERM as xterm-256color as used by standard terminals to fix this issue
	cmd.Env = append(os.Environ(), termEnvVariable)

	// If LANG environment variable is not set, shell defaults to POSIX which can contain 256 single-byte characters.
	// Setting C.UTF-8 as default LANG environment variable as Session Manager supports UTF-8 encoding only.
	langEnvVariableValue := os.Getenv(langEnvVariableKey)
	if langEnvVariableValue == "" {
		cmd.Env = append(cmd.Env, langEnvVariable)
	}

	// Get the uid and gid of the runas user.
	uid, gid, groups, err := getUserCredentials(runAsUser, shellCommand, shellCommandArgs)
	if err != nil {
		return nil, err
	}

	cmd.SysProcAttr = &syscall.SysProcAttr{}
	// Changed NoSetGroups = true (was NoSetGroups = false) because it doesn't work when set to true
	cmd.SysProcAttr.Credential = &syscall.Credential{Uid: uid, Gid: gid, Groups: groups, NoSetGroups: true}

	// Setting home environment variable for RunAs user
	runAsUserHomeEnvVariable := homeEnvVariable + runAsUser
	cmd.Env = append(cmd.Env, runAsUserHomeEnvVariable)

	// Setting cwd of the command to be the user's home directory
	if currentUser, err := user.Lookup(runAsUser); err != nil {
		return nil, fmt.Errorf("failed to lookup user: %s", runAsUser)
	} else {
		cmd.Dir = currentUser.HomeDir
	}
	//cmd.Dir = fmt.Sprintf("/home/%s", runAsUser)

	return cmd, nil
}

func (p *PseudoTerminal) Done() <-chan struct{} {
	doneChan := make(chan struct{})

	go func() {
		defer close(doneChan)

		if p.command == nil {
			return
		} else if err := p.command.Wait(); err != nil {
			p.logger.Errorf("pty command exited with err: %s", err)
			if exitError, ok := err.(*exec.ExitError); ok {
				exitCode := exitError.ExitCode()
				p.logger.Errorf("pty cmd exited with non-zero exit code %d err: %s", exitCode, err)
			} else {
				p.logger.Errorf("pty command exited with unknown exit code")
			}
		}
	}()

	return doneChan
}

func (p *PseudoTerminal) Closed() bool {
	if p.command != nil {
		return p.command.ProcessState.Exited()
	} else {
		return false
	}
}

func (p *PseudoTerminal) Kill() {
	if p.ptyFile != nil {
		if err := p.ptyFile.Close(); err != nil {
			p.logger.Errorf("failed to close pty: %s", err)
		}
		p.ptyFile = nil
	}
}

func (p *PseudoTerminal) StdIn() *os.File {
	return p.ptyFile
}

func (p *PseudoTerminal) StdOut() *os.File {
	return p.ptyFile
}

// SetSize sets size of console terminal window.
func (p *PseudoTerminal) SetSize(cols, rows uint32) error {
	winSize := pty.Winsize{
		Cols: uint16(cols),
		Rows: uint16(rows),
	}

	if err := pty.Setsize(p.ptyFile, &winSize); err != nil {
		return fmt.Errorf("set terminal window size failed: %s", err)
	}
	return nil
}

// DoesUserExist checks if given user already exists
func doesUserExist(username string, shellCommand string, shellCommandArgs []string) error {
	shellCmdArgs := append(shellCommandArgs, fmt.Sprintf("id %s", username))
	cmd := exec.Command(shellCommand, shellCmdArgs...)
	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			// The program has exited with an exit code != 0
			return fmt.Errorf("encountered an error while checking for %s: %s", username, exitErr)
		} else {
			return fmt.Errorf("failed to check if user already exists")
		}
	}
	return nil
}

// Get the default shell for the user based on configuration in /etc/passwd file.
// https://unix.stackexchange.com/a/352320
func getDefaultShellForUser(user string) (string, error) {
	defaultShell := defaultShellCommand
	defaultShellCmd := exec.Command(defaultShell, "-c", fmt.Sprintf("getent passwd %s | awk -F: '{print $NF}'", user))

	if out, err := defaultShellCmd.Output(); err != nil {
		return defaultShell, fmt.Errorf("failed to get default shell for user %s: %s", user, err)
	} else if len(out) == 0 {
		return defaultShell, nil
	} else {
		shellCmdPath := strings.TrimSpace(string(out))
		// Use just the shell command and not full path because exec.Command()
		// will find the correct path to use by searching for the command in $PATH
		defaultShell = path.Base(shellCmdPath) // /bin/bash -> bash
		return defaultShell, nil
	}
}

// getUserCredentials returns the uid, gid and groups associated to the runas user.
func getUserCredentials(sessionUser string, shellCommand string, shellCommandArgs []string) (uint32, uint32, []uint32, error) {
	uidCmdArgs := append(shellCommandArgs, fmt.Sprintf("id -u %s", sessionUser))
	cmd := exec.Command(shellCommand, uidCmdArgs...)
	out, err := cmd.Output()
	if err != nil {
		return 0, 0, nil, fmt.Errorf("failed to retrieve uid for %s: %v", sessionUser, err)
	}

	uid, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return 0, 0, nil, fmt.Errorf("%s not found: %v", sessionUser, err)
	}

	gidCmdArgs := append(shellCommandArgs, fmt.Sprintf("id -g %s", sessionUser))
	cmd = exec.Command(shellCommand, gidCmdArgs...)
	out, err = cmd.Output()
	if err != nil {
		return 0, 0, nil, fmt.Errorf("failed to retrieve gid for %s: %v", sessionUser, err)
	}

	gid, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return 0, 0, nil, fmt.Errorf("%s not found: %v", sessionUser, err)
	}

	// Get the list of associated groups
	groupNamesCmdArgs := append(shellCommandArgs, fmt.Sprintf("id %s", sessionUser))
	cmd = exec.Command(shellCommand, groupNamesCmdArgs...)
	out, err = cmd.Output()
	if err != nil {
		return 0, 0, nil, fmt.Errorf("failed to retrieve groups for %s: %v", sessionUser, err)
	}

	// Example format of output: uid=1873601143(ssm-user) gid=1873600513(domain users) groups=1873600513(domain users),1873601620(joiners),1873601125(aws delegated add workstations to domain users)
	// Extract groups from the output
	groupsIndex := strings.Index(string(out), groupsIdentifier)
	var groupIds []uint32

	if groupsIndex > 0 {
		// Extract groups names and ids from the output
		groupNamesAndIds := strings.Split(string(out)[groupsIndex+len(groupsIdentifier):], ",")

		// Extract group ids from the output
		for _, value := range groupNamesAndIds {
			groupId, err := strconv.Atoi(strings.TrimSpace(value[:strings.Index(value, "(")]))
			if err != nil {
				return 0, 0, nil, fmt.Errorf("failed to retrieve group id from %s: %v", value, err)
			}

			groupIds = append(groupIds, uint32(groupId))
		}
	}

	return uint32(uid), uint32(gid), groupIds, nil
}
