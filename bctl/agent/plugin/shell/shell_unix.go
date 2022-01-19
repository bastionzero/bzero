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

//go:build darwin || freebsd || linux || netbsd || openbsd
// +build darwin freebsd linux netbsd openbsd

// Package shell implements session shell plugin.
package shell

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"

	"bastionzero.com/bctl/v1/bctl/agent/plugin/shell/execcmd"
	"bastionzero.com/bctl/v1/bctl/agent/utility"
	"bastionzero.com/bctl/v1/bzerolib/logger"
	"github.com/creack/pty"
)

var ptyFile *os.File

const (
	termEnvVariable       = "TERM=xterm-256color"
	langEnvVariable       = "LANG=C.UTF-8"
	langEnvVariableKey    = "LANG"
	startRecordSessionCmd = "script"
	newLineCharacter      = "\n"
	catCmd                = "cat"
	scriptFlag            = "-c"
	homeEnvVariable       = "HOME=/home/"
	groupsIdentifier      = "groups="
)

//StartPty starts pty and provides handles to stdin and stdout
// isSessionLogger determines whether its a customer shell or shell used for logging.
func StartPty(
	logger *logger.Logger,
	runAsUser string,
	commandstr string,
	plugin *ShellPlugin) (err error) {

	logger.Info("Starting pty")
	//Start the command with a pty
	var cmd *exec.Cmd

	if strings.TrimSpace(commandstr) == "" {
		cmd = exec.Command("sh")
	} else {
		commandArgs := append(utility.ShellPluginCommandArgs, commandstr)
		cmd = exec.Command("sh", commandArgs...)
	}

	//TERM is set as linux by pty which has an issue where vi editor screen does not get cleared.
	//Setting TERM as xterm-256color as used by standard terminals to fix this issue
	cmd.Env = append(os.Environ(), termEnvVariable)

	//If LANG environment variable is not set, shell defaults to POSIX which can contain 256 single-byte characters.
	//Setting C.UTF-8 as default LANG environment variable as Session Manager supports UTF-8 encoding only.
	langEnvVariableValue := os.Getenv(langEnvVariableKey)
	if langEnvVariableValue == "" {
		cmd.Env = append(cmd.Env, langEnvVariable)
	}

	var sessionUser string

	if strings.TrimSpace(runAsUser) == "" {
		return errors.New("please set the RunAs default user")
	}

	// Check if user exists
	if userExists, _ := utility.DoesUserExist(runAsUser); !userExists {
		// if user does not exist, fail the session
		return fmt.Errorf("failed to start pty since RunAs user %s does not exist", runAsUser)
	}

	sessionUser = runAsUser

	// Get the uid and gid of the runas user.
	uid, gid, groups, err := getUserCredentials(logger, sessionUser)
	if err != nil {
		return err
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{}
	// Changed NoSetGroups = true (was NoSetGroups = false) because it doesn't work when set to true
	cmd.SysProcAttr.Credential = &syscall.Credential{Uid: uid, Gid: gid, Groups: groups, NoSetGroups: true}

	// Setting home environment variable for RunAs user
	runAsUserHomeEnvVariable := homeEnvVariable + sessionUser
	cmd.Env = append(cmd.Env, runAsUserHomeEnvVariable)

	ptyFile, err = pty.Start(cmd)

	if err != nil {
		logger.Errorf("Failed to start pty: %s\n", err)
		return fmt.Errorf("Failed to start pty: %s", err)
	}

	plugin.stdin = ptyFile
	plugin.stdout = ptyFile
	plugin.runAsUser = sessionUser
	plugin.execCmd = execcmd.NewExecCmd(cmd)

	return nil
}

// SetSize sets size of console terminal window.
func SetSize(logger *logger.Logger, ws_col, ws_row uint32) (err error) {
	winSize := pty.Winsize{
		Cols: uint16(ws_col),
		Rows: uint16(ws_row),
	}

	if err := pty.Setsize(ptyFile, &winSize); err != nil {
		return fmt.Errorf("set pty size failed: %s", err)
	}
	return nil
}

// getUserCredentials returns the uid, gid and groups associated to the runas user.
func getUserCredentials(logger *logger.Logger, sessionUser string) (uint32, uint32, []uint32, error) {
	uidCmdArgs := append(utility.ShellPluginCommandArgs, fmt.Sprintf("id -u %s", sessionUser))
	cmd := exec.Command(utility.ShellPluginCommandName, uidCmdArgs...)
	out, err := cmd.Output()
	if err != nil {
		logger.Errorf("Failed to retrieve uid for %s: %v", sessionUser, err)
		return 0, 0, nil, err
	}

	uid, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		logger.Errorf("%s not found: %v", sessionUser, err)
		return 0, 0, nil, err
	}

	gidCmdArgs := append(utility.ShellPluginCommandArgs, fmt.Sprintf("id -g %s", sessionUser))
	cmd = exec.Command(utility.ShellPluginCommandName, gidCmdArgs...)
	out, err = cmd.Output()
	if err != nil {
		logger.Errorf("Failed to retrieve gid for %s: %v", sessionUser, err)
		return 0, 0, nil, err
	}

	gid, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		logger.Errorf("%s not found: %v", sessionUser, err)
		return 0, 0, nil, err
	}

	// Get the list of associated groups
	groupNamesCmdArgs := append(utility.ShellPluginCommandArgs, fmt.Sprintf("id %s", sessionUser))
	cmd = exec.Command(utility.ShellPluginCommandName, groupNamesCmdArgs...)
	out, err = cmd.Output()
	if err != nil {
		logger.Errorf("Failed to retrieve groups for %s: %v", sessionUser, err)
		return 0, 0, nil, err
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
				logger.Errorf("Failed to retrieve group id from %s: %v", value, err)
				return 0, 0, nil, err
			}

			groupIds = append(groupIds, uint32(groupId))
		}
	}

	// Make sure they are non-zero valid positive ids
	// As user 'root' is by convention uid=0, gid=0 this disallows starting the terminal as root
	if uid > 0 && gid > 0 {
		return uint32(uid), uint32(gid), groupIds, nil
	}

	return 0, 0, nil, errors.New("invalid uid and gid")
}
