// Copyright 2019 Amazon.com, Inc. or its affiliates. All Rights Reserved.
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
// Modifications Copyright (C) 2022 BastionZero Inc.  The BastionZero SSM Agent
// is licensed under the Apache 2.0 License.

//go:build darwin || freebsd || linux || netbsd || openbsd
// +build darwin freebsd linux netbsd openbsd

// utility package implements all the shared methods between clients.
package utility

import (
	"fmt"
	"os/exec"
	"path"
	"strings"
)

var ShellPluginCommandName = "sh"
var ShellPluginBashCommandName = "/bin/bash"
var ShellPluginCommandArgs = []string{"-c"}

var DefaultRunAsUser = "bzuser"

const (
	sudoersFile     = "/etc/sudoers.d/ssm-agent-users"
	sudoersFileMode = 0440
	fs_ioc_getflags = uintptr(0x80086601)
	fs_ioc_setflags = uintptr(0x40086602)
	FS_APPEND_FL    = 0x00000020 /* writes to file may only append */
	FS_RESET_FL     = 0x00000000 /* reset file property */
)

// DoesUserExist checks if given user already exists
func DoesUserExist(username string) (bool, error) {

	shellCmdArgs := append(ShellPluginCommandArgs, fmt.Sprintf("id %s", username))
	cmd := exec.Command(ShellPluginCommandName, shellCmdArgs...)
	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			// The program has exited with an exit code != 0
			return false, fmt.Errorf("encountered an error while checking for %s: %v", DefaultRunAsUser, exitErr.Error())
		}
		return false, nil
	}
	return true, nil
}

// WhoAmI returns the current username that the agent is running under
func WhoAmI() (string, error) {
	cmdstr := "whoami"
	shellCmdArgs := append(ShellPluginCommandArgs, cmdstr)
	cmd := exec.Command(ShellPluginCommandName, shellCmdArgs...)
	stdout, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			// The program has exited with an exit code != 0
			return "", fmt.Errorf("encountered an error while running command %v : %v", cmdstr, exitErr.Error())
		}
		return "", nil
	}

	return strings.TrimSpace(string(stdout)), nil
}

// Get the default shell for the user based on configuration in /etc/passwd
// file.
// https://unix.stackexchange.com/a/352320
func TryGetDefaultShellForUser(user string) (string, error) {
	defaultShellCmd := exec.Command("sh", "-c", fmt.Sprintf("getent passwd %s | awk -F: '{print $NF}'", user))
	if out, err := defaultShellCmd.Output(); err == nil {
		shellCmdPath := strings.TrimSpace(string(out))
		// Use just the shell command and not full path because exec.Command()
		// will find the correct path to use by searching for the command in $PATH
		defaultShell := path.Base(shellCmdPath) // /bin/bash -> bash
		return defaultShell, nil
	} else {
		return "", fmt.Errorf("Failed to get default shell for user %s: %s.", user, err)
	}
}
