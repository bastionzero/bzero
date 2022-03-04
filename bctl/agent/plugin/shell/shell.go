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
	"sync"
	"time"

	"bastionzero.com/bctl/v1/bctl/agent/config"
	plgn "bastionzero.com/bctl/v1/bctl/agent/plugin"
	"bastionzero.com/bctl/v1/bctl/agent/plugin/shell/execcmd"

	"bastionzero.com/bctl/v1/bzerolib/logger"
	bzshell "bastionzero.com/bctl/v1/bzerolib/plugin/shell"
	"bastionzero.com/bctl/v1/bzerolib/ringbuffer"
	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
	"gopkg.in/tomb.v2"
)

// ShellPlugin - Allows launching an interactive shell on the host which the agent is running on.
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
	tmb    *tomb.Tomb // datachannel's tomb
	logger *logger.Logger
	// name             string
	stdin            *os.File
	stdout           *os.File
	execCmd          execcmd.IExecCmd
	streamOutputChan chan smsg.StreamMessage
	shellStarted     bool
	runAsUser        string
	stdoutbuff       *ringbuffer.RingBuffer
	stdoutbuffMutex  sync.Mutex
}

// New returns a new instance of the Shell Plugin
func New(parentTmb *tomb.Tomb,
	logger *logger.Logger,
	ch chan smsg.StreamMessage,
	payload []byte) (*ShellPlugin, error) {

	// Unmarshal the Syn payload
	var configPayload bzshell.ShellOpenMessage
	if err := json.Unmarshal(payload, &configPayload); err != nil {
		return &ShellPlugin{}, fmt.Errorf("malformed Shell plugin SYN payload %v", string(payload))
	}

	var plugin = ShellPlugin{
		runAsUser:        configPayload.TargetUser,
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
	if plgn.PluginName(parsedAction[0]) != plgn.Shell {
		return "", []byte{}, fmt.Errorf("malformed action: expected 'shell/.*' got %s", action)
	}

	shellAction := parsedAction[1]

	actionPayloadSafe := []byte(string(actionPayload))

	switch bzshell.ShellAction(shellAction) {
	case bzshell.ShellOpen:
		// We ignore the RunAsUser in the second Shell/Open message since it was set in the first one processed by .New.
		//  This is important because the RunAsUser is part of policy and the policy check happens in the first Shell/Open message.
		if err := k.open(); err != nil {
			errorString := fmt.Errorf("unable to start shell: %s", err)
			k.logger.Error(errorString)
			time.Sleep(2 * time.Second)
			return "", []byte{}, errorString
		}
	case bzshell.ShellClose:
		if err := k.close(); err != nil {
			rerr := fmt.Errorf("shell stop failed %v", err)
			k.logger.Error(rerr)
			return action, []byte{}, rerr
		}
	case bzshell.ShellInput:
		var shellInput bzshell.ShellInputMessage

		if err := json.Unmarshal(actionPayloadSafe, &shellInput); err != nil {
			rerr := fmt.Errorf("malformed shell input payload %v", actionPayload)
			k.logger.Error(rerr)
			return action, []byte{}, rerr
		}

		if err := k.shellInput(shellInput); err != nil {
			rerr := fmt.Errorf("write to stdin failed %v", err)
			k.logger.Error(rerr)
			return action, []byte{}, rerr
		}
	case bzshell.ShellResize:
		var shellResize bzshell.ShellResizeMessage

		if err := json.Unmarshal(actionPayloadSafe, &shellResize); err != nil {
			rerr := fmt.Errorf("malformed shell resize payload %v", actionPayload)
			k.logger.Error(rerr)
			return action, []byte{}, rerr
		}

		if err := k.setSize(shellResize.Cols, shellResize.Rows); err != nil {
			rerr := fmt.Errorf("shell resize failed %v", err)
			k.logger.Error(rerr)
			return action, []byte{}, rerr
		}
	case bzshell.ShelllReplay:
		var shellReplay bzshell.ShellReplayMessage

		if err := json.Unmarshal(actionPayloadSafe, &shellReplay); err != nil {
			rerr := fmt.Errorf("malformed shell replay output payload %v", actionPayload)
			k.logger.Error(rerr)
			return action, []byte{}, rerr
		}

		outbuff := make([]byte, config.ShellStdOutBuffCapacity)
		k.stdoutbuffMutex.Lock()
		n, err := k.stdoutbuff.Read(outbuff)
		k.stdoutbuffMutex.Unlock()

		if err != nil {
			return action, []byte{}, fmt.Errorf("failed to read from stdout buff for shell replay %v", err)
		}
		return action, outbuff[0:n], nil
	}

	return action, []byte{}, nil
}

// Ready returns if the shell is running and can be interacted with
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
	if k.shellStarted {
		return fmt.Errorf("attempted to start the shell but a call to open a shell has already been made")
	}
	k.shellStarted = true
	commands := ""

	// Catch that the tomb is dying and signal shell to close
	go func() {
		<-k.tmb.Dying()
		k.logger.Errorf("shell plugin is terminating")
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
	if !k.Ready() {
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

	k.stdoutbuff = ringbuffer.New(config.ShellStdOutBuffCapacity)

	stdoutBytes := make([]byte, config.StreamDataPayloadSize)
	reader := bufio.NewReader(k.stdout)

	// Wait for all input commands to run.
	time.Sleep(time.Second)

	sequenceNumber := 1

	for {
		stdoutBytesLen, err := reader.Read(stdoutBytes)

		if err != nil {
			message := smsg.StreamMessage{
				Type:           string(smsg.ShellQuit),
				RequestId:      "shell", // not needed for shell because we aren't multiplexing sessions over a shared data channel
				SequenceNumber: sequenceNumber,
				Content:        "",
				LogId:          "", // only used for kube plugin
			}

			k.streamOutputChan <- message
			sequenceNumber++

			fmt.Println("WritePump failed when reading from stdout: \n", err)
			logger.Errorf("Stacktrace:\n%s", debug.Stack())
			return config.ErrorExitCode
		}

		k.stdoutbuffMutex.Lock()
		k.stdoutbuff.Write(stdoutBytes[:stdoutBytesLen])
		k.stdoutbuffMutex.Unlock()

		str := base64.StdEncoding.EncodeToString(stdoutBytes[:stdoutBytesLen])

		message := smsg.StreamMessage{
			Type:           string(smsg.ShellStdOut),
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
