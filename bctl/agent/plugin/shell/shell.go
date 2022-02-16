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

package shell

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"runtime/debug"
	"strings"
	"time"

	"bastionzero.com/bctl/v1/bctl/agent/config"
	"bastionzero.com/bctl/v1/bctl/agent/datachannel"
	"bastionzero.com/bctl/v1/bctl/agent/plugin/shell/execcmd"

	"bastionzero.com/bctl/v1/bzerolib/logger"
	bzshell "bastionzero.com/bctl/v1/bzerolib/plugin/shell"
	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
	"gopkg.in/tomb.v2"
)

// ShellPlugin - Allows launching an interactive shell on the host which the agent is running on.
//
//
//   New - Configures the shell including setting the RunAsUser but doesn't launch it
//   Receive - receives MRZAP actions and dispatches them to the correct methods
//
//		Current shell actions
// 			ShellOpen   KeysplittingAction = "shell/open"
// 			ShellClose  KeysplittingAction = "shell/close"
// 			ShellInput  KeysplittingAction = "shell/input"
// 			ShellResize KeysplittingAction = "shell/resize"
// 			FudDownload KeysplittingAction = "fud/download" -- Not currently implemented
// 			FudUpload   KeysplittingAction = "fud/upload" -- Not currently implemented
// 		 		- sourced from https://github.com/bastionzero/bzero-ssm-agent/blob/bzero-dev/agent/keysplitting/contracts/model.go#L106
//
//  User interaction works as follows:
//  	user keypresses --> DataChannel --> plugin.Receive(shell/input) --> p.ShellInput(..) --> pty.stdIn
//  	user terminal <-- streamOutputChan <-- p.writePump(...) <-- pty.stdOut <-- terminal output
//
// We follow a pattern of wrapping the pty functions in ShellPlugin
// 		plugin.open --> pty.Start() Creates New pty
// 		plugin.setSize --> pty.SetSize() ReSizes the pty
//  	plugin.shellInput --> pty.stdIn
//		plugin.close --> pty.ptyfile.close()
//
//		This should allow use to mock at the level of the ShellPlugin since none of the pty details are exposed

type ShellPlugin struct {
	tmb              *tomb.Tomb // datachannel's tomb
	logger           *logger.Logger
	name             string
	stdin            *os.File
	stdout           *os.File
	execCmd          execcmd.IExecCmd
	streamOutputChan chan smsg.StreamMessage
	shellStarted     bool
	runAsUser        string
}

// New returns a new instance of the Shell Plugin
func New(parentTmb *tomb.Tomb,
	logger *logger.Logger,
	ch chan smsg.StreamMessage,
	payload []byte) (*ShellPlugin, error) {

	// Unmarshal the Syn payload
	var configPayload bzshell.ShellConfigParams
	if err := json.Unmarshal(payload, &configPayload); err != nil {
		return &ShellPlugin{}, fmt.Errorf("malformed Shell plugin SYN payload %v", string(payload))
	}

	var plugin = ShellPlugin{
		runAsUser:        configPayload.RunAsUser,
		shellStarted:     false,
		logger:           logger,
		tmb:              parentTmb, // if datachannel dies, so should we
		streamOutputChan: ch,
	}

	return &plugin, nil
}

// Receive takes input from a client using the MRZAP datachannel and returns output via the MRZAP datachannel
func (k *ShellPlugin) Receive(action string, actionPayload []byte) (string, []byte, error) {
	k.logger.Infof("Plugin received Data message with %v action", action)

	// parse action
	parsedAction := strings.Split(action, "/")
	if len(parsedAction) < 2 {
		return "", []byte{}, fmt.Errorf("malformed action: %s", action)
	}
	if datachannel.PluginName(parsedAction[0]) != datachannel.Shell {
		return "", []byte{}, fmt.Errorf("malformed action: expected 'shell/.*' got %s", action)
	}

	shellAction := parsedAction[1]

	// TODO: The below line removes the extra, surrounding quotation marks that get added at some point in the marshal/unmarshal
	// so it messes up the umarshalling into a valid action payload.  We need to figure out why this is happening
	// so that we can murder its family
	if len(actionPayload) > 0 {
		actionPayload = actionPayload[1 : len(actionPayload)-1]
	}

	// Json unmarshalling encodes bytes in base64
	actionPayloadSafe, base64Err := base64.StdEncoding.DecodeString(string(actionPayload))
	if base64Err != nil {
		k.logger.Errorf("error decoding actionPayload: %v", base64Err)
		return "", []byte{}, base64Err
	}

	switch bzshell.ShellAction(shellAction) {
	case bzshell.ShellOpen:
		if err := k.open(); err != nil {
			errorString := fmt.Errorf("Unable to start shell: %s", err)
			k.logger.Error(errorString)
			time.Sleep(2 * time.Second)
			return "", []byte{}, errorString
		}
	case bzshell.ShellClose:
		if err := k.close(); err != nil {
			rerr := fmt.Errorf("Shell stop failed %v", err)
			k.logger.Error(rerr)
			return action, []byte{}, rerr
		}
	case bzshell.ShellInput:
		var shellInput bzshell.ShellInputMessage

		if err := json.Unmarshal(actionPayloadSafe, &shellInput); err != nil {
			rerr := fmt.Errorf("Malformed shell input payload %v", actionPayload)
			k.logger.Error(rerr)
			return action, []byte{}, rerr
		}

		if err := k.shellInput(shellInput); err != nil {
			rerr := fmt.Errorf("Write to stdin failed %v", err)
			k.logger.Error(rerr)
			return action, []byte{}, rerr
		}
	case bzshell.ShellResize:
		var shellResize bzshell.ShellResizeMessage

		if err := json.Unmarshal(actionPayloadSafe, &shellResize); err != nil {
			rerr := fmt.Errorf("Malformed shell resize payload %v", actionPayload)
			k.logger.Error(rerr)
			return action, []byte{}, rerr
		}

		if err := k.setSize(shellResize.Cols, shellResize.Rows); err != nil {
			rerr := fmt.Errorf("Shell resize failed %v", err)
			k.logger.Error(rerr)
			return action, []byte{}, rerr
		}
	}

	return action, []byte{}, nil
}

func (k *ShellPlugin) Ready() bool {
	return !(k.stdin == nil || k.stdout == nil)
}

var startPty = func(
	logger *logger.Logger,
	runAsUser string,
	commands string,
	plugin *ShellPlugin) (err error) {

	return StartPty(logger, runAsUser, commands, plugin)
}

func (k *ShellPlugin) open() error {
	// If this method "open" is called twice is means something has gone very
	//  wrong and failing early is the safest action.
	if k.shellStarted == true {
		return fmt.Errorf("Attempted to start the shell but a call to open a shell has already been made")
	}
	k.shellStarted = true
	commands := ""

	// Catch that the tomb is dying and signal shell to close
	go func() {
		<-k.tmb.Dying()
		if k.execCmd != nil {
			if err := k.execCmd.Kill(); err != nil {
				k.logger.Errorf("unable to terminate pty: %s", err)
			}
		}
	}()

	// Start pseudo terminal
	if err := startPty(k.logger, k.runAsUser, commands, k); err != nil {
		return err
	}

	// Start to read from shell and write to datachannel
	k.logger.Debugf("Start separate go routine to read from pty stdout and write to data channel")
	done := make(chan int, 1)
	go func() {
		done <- k.writePump(k.logger)
	}()

	return nil
}

// ctose closes pty file
func (k *ShellPlugin) close() (err error) {
	k.logger.Info("Stopping pty")
	if err := ptyFile.Close(); err != nil {
		if err, ok := err.(*os.PathError); ok && err.Err != os.ErrClosed {
			return fmt.Errorf("unable to close ptyFile. %s", err)
		}
	}
	return nil
}

// shellInput passes payload byte stream to shell stdin
func (k *ShellPlugin) shellInput(shellInput bzshell.ShellInputMessage) error {
	if k.Ready() == false {
		// This is to handle scenario when cli/console starts sending size data but pty has not been started yet
		// Since packets are rejected, cli/console will resend these packets until pty starts successfully in separate thread
		k.logger.Tracef("Pty unavailable. Reject incoming message packet")
		return errors.New("message handler is not ready, rejecting incoming packet")
	}

	k.logger.Tracef("Input message received: ")
	instr, err := base64.StdEncoding.DecodeString(string(shellInput.Data))
	if err != nil {
		k.logger.Errorf("Shell stdout stream base64 decode failed: %v", err)
		return err
	}
	if _, err := k.stdin.Write(instr); err != nil {
		k.logger.Errorf("Unable to write to stdin, err: %v.", err)
		return err
	}

	return nil
}

// setSize resizes the pseudo-terminal pty
func (k *ShellPlugin) setSize(cols, rows uint32) (err error) {
	k.logger.Tracef("Pty Resize data received: cols: %d, rows: %d", cols, rows)
	if err := SetSize(k.logger, cols, rows); err != nil {
		k.logger.Errorf("Unable to set pty size: %s", err)
		return err
	}
	return nil
}

// writePump reads from pty stdout and writes to data channel.
func (k *ShellPlugin) writePump(logger *logger.Logger) int {
	defer func() {
		if err := recover(); err != nil {
			fmt.Println("WritePump thread crashed with message: \n", err)
			logger.Errorf("Stacktrace:\n%s", debug.Stack())
		}
	}()

	stdoutBytes := make([]byte, config.StreamDataPayloadSize)
	reader := bufio.NewReader(k.stdout)

	// Wait for all input commands to run.
	time.Sleep(time.Second)

	sequenceNumber := 1

	for {
		stdoutBytesLen, err := reader.Read(stdoutBytes)
		if err != nil {
			fmt.Println("WritePump failed when reading from stdout: \n", err)
			logger.Errorf("Stacktrace:\n%s", debug.Stack())
			return config.ErrorExitCode
		}

		str := base64.StdEncoding.EncodeToString(stdoutBytes[:stdoutBytesLen])

		message := smsg.StreamMessage{
			Type:           string(smsg.StdOut),
			RequestId:      "shell", // not needed for shell because we aren't multiplexing sessions over a shared data channel
			SequenceNumber: sequenceNumber,
			Content:        str,
			LogId:          "", // only used for kube plugin
		}

		k.streamOutputChan <- message
		sequenceNumber++

		// Wait for stdout to process more data
		time.Sleep(time.Millisecond)
	}
}
