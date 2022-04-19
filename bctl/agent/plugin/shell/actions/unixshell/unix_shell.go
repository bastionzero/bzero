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

package unixshell

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime/debug"
	"sync"
	"time"

	"bastionzero.com/bctl/v1/bctl/agent/config"
	"bastionzero.com/bctl/v1/bctl/agent/plugin/shell/actions/unixshell/execcmd"

	"bastionzero.com/bctl/v1/bzerolib/logger"
	bzshell "bastionzero.com/bctl/v1/bzerolib/plugin/shell"
	"bastionzero.com/bctl/v1/bzerolib/ringbuffer"
	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
	"gopkg.in/tomb.v2"
)

// UnixShell - Allows launching an interactive shell on the host which the agent is running on. Implements IShellAction
//
//   New - Configures the shell including setting the RunAsUser but doesn't launch it
//   Receive - receives MRZAP actions and dispatches them to the correct methods
//
//  User interaction works as follows:
//  	user keypresses --> DataChannel --> plugin.Receive(shell/input) --> p.action.Receive(shell/input) -> p.ShellInput(..) --> pty.stdIn
//  	user terminal <-- streamOutputChan <-- p.writePump(...) <-- pty.stdOut <-- terminal output
//
// We follow a pattern of wrapping the pty functions in UnixShell
// 		open --> pty.Start() Creates New pty
// 		setSize --> pty.SetSize() ReSizes the pty
//  	shellInput --> pty.stdIn
//		close --> pty.ptyfile.close()
//
//		This should allow use to mock at the level of the UnixShell since none of the pty details are exposed
type UnixShell struct {
	tmb                  *tomb.Tomb // datachannel's tomb
	logger               *logger.Logger
	stdin                *os.File
	stdout               *os.File
	execCmd              execcmd.IExecCmd
	execCmdDone          chan int // exit code
	streamOutputChan     chan smsg.StreamMessage
	shellStarted         bool
	runAsUser            string
	stdoutbuff           *ringbuffer.RingBuffer
	stdoutbuffMutex      sync.Mutex
	streamSequenceNumber int
}

// New returns a new instance of the UnixShell
func New(
	parentTmb *tomb.Tomb,
	logger *logger.Logger,
	ch chan smsg.StreamMessage,
	runAsUser string) (*UnixShell, error) {
	var unixShell = UnixShell{
		runAsUser:            runAsUser,
		shellStarted:         false,
		logger:               logger,
		tmb:                  parentTmb, // if datachannel dies, so should we
		streamOutputChan:     ch,
		streamSequenceNumber: 1,
		execCmdDone:          make(chan int, 1),
	}

	return &unixShell, nil
}

// Receive takes input from a client using the MRZAP datachannel and returns output via the MRZAP datachannel
func (k *UnixShell) Receive(action string, actionPayload []byte) (string, []byte, error) {
	k.logger.Infof("Plugin received Data message with %v action", action)

	switch bzshell.ShellSubAction(action) {
	case bzshell.ShellOpen:
		// We ignore the RunAsUser in the Shell/Open message since it was set by
		// the plugin when it processes the SYN message. This is important
		// because the RunAsUser is part of policy and the policy check happens
		// based on the SYN message when the data channel is first opened.
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

		if err := json.Unmarshal(actionPayload, &shellInput); err != nil {
			rerr := fmt.Errorf("malformed shell input payload %v: %s", actionPayload, err)
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

		if err := json.Unmarshal(actionPayload, &shellResize); err != nil {
			rerr := fmt.Errorf("malformed shell resize payload %v", actionPayload)
			k.logger.Error(rerr)
			return action, []byte{}, rerr
		}

		if err := k.setSize(shellResize.Cols, shellResize.Rows); err != nil {
			rerr := fmt.Errorf("shell resize failed %v", err)
			k.logger.Error(rerr)
			return action, []byte{}, rerr
		}
	case bzshell.ShellReplay:
		var shellReplay bzshell.ShellReplayMessage

		if err := json.Unmarshal(actionPayload, &shellReplay); err != nil {
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
	default:
		return action, []byte{}, fmt.Errorf("unrecognized shell action received: %s", action)
	}

	return action, []byte{}, nil
}

// Ready returns if the shell is running and can be interacted with
func (k *UnixShell) Ready() bool {
	return !(k.stdin == nil || k.stdout == nil)
}

var startPty = func(
	logger *logger.Logger,
	runAsUser string,
	commands string,
	plugin *UnixShell) (err error) {

	return StartPty(logger, runAsUser, commands, plugin)
}

func (k *UnixShell) open() error {
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
		k.logger.Info("shell plugin is terminating")
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

	// Start a go routine to wait for the pty cmd to exit and close the
	// execCmdDone channel
	go func() {
		if err := k.execCmd.Wait(); err != nil {
			k.logger.Errorf("pty command exited with err: %s", err)
			if exitError, ok := err.(*exec.ExitError); ok {
				exitCode := exitError.ExitCode()
				k.logger.Errorf("pty cmd exited with non-zero exit code %d err: %s", exitCode, err)
				k.execCmdDone <- exitCode
			} else {
				k.logger.Errorf("pty command exited with unknown exit code")
				k.execCmdDone <- -1
			}
		} else {
			k.execCmdDone <- 0
		}

		k.execCmd = nil
		close(k.execCmdDone)
	}()

	// Start to read from shell and write to datachannel
	k.logger.Debugf("Start separate go routine to read from pty stdout and write to data channel")
	done := make(chan int, 1)
	go func() {
		done <- k.writePump(k.logger)
	}()

	return nil
}

// ctose closes pty file
func (k *UnixShell) close() (err error) {
	k.logger.Info("Stopping pty")
	if err := ptyFile.Close(); err != nil {
		if err, ok := err.(*os.PathError); ok && err.Err != os.ErrClosed {
			return fmt.Errorf("unable to close ptyFile. %s", err)
		}
	}
	return nil
}

// shellInput passes payload byte stream to shell stdin
func (k *UnixShell) shellInput(shellInput bzshell.ShellInputMessage) error {
	if !k.Ready() {
		// This is to handle scenario when cli/console starts sending size data but pty has not been started yet
		// Since packets are rejected, cli/console will resend these packets until pty starts successfully in separate thread
		k.logger.Debug("Unix shell action is not ready. Rejecting incoming message packet")
		return errors.New("unix shell input handler is not ready, rejecting incoming packet")
	}

	k.logger.Tracef("Input message received: ")
	if _, err := k.stdin.Write(shellInput.Data); err != nil {
		k.logger.Errorf("Unable to write to stdin, err: %v.", err)
		return err
	}

	return nil
}

// setSize resizes the pseudo-terminal pty
func (k *UnixShell) setSize(cols, rows uint32) (err error) {
	k.logger.Debugf("Pty Resize data received: cols: %d, rows: %d", cols, rows)
	if err := SetSize(k.logger, cols, rows); err != nil {
		k.logger.Errorf("Unable to set pty size: %s", err)
		return err
	}
	return nil
}

// writePump reads from pty stdout and writes to data channel.
func (k *UnixShell) writePump(logger *logger.Logger) int {
	defer func() {
		if err := recover(); err != nil {
			logger.Errorf("WritePump thread crashed with message: \n", err)
			logger.Errorf("Stacktrace:\n%s", debug.Stack())
		}
	}()

	k.stdoutbuff = ringbuffer.New(config.ShellStdOutBuffCapacity)

	stdoutBytes := make([]byte, config.StreamDataPayloadSize)
	reader := bufio.NewReader(k.stdout)

	// Wait for all input commands to run.
	time.Sleep(time.Second)

	for {
		select {
		case exitCode := <-k.execCmdDone:
			// Handle pty exit by sending shell quit stream message
			k.logger.Infof("Pty exited with code %d", exitCode)
			k.sendStreamMessage(smsg.ShellQuit, "")
			return exitCode
		default:
			stdoutBytesLen, err := reader.Read(stdoutBytes)

			if err != nil {
				k.sendStreamMessage(smsg.ShellQuit, "")

				logger.Errorf("WritePump failed when reading from stdout: \n", err)
				return config.ErrorExitCode
			}

			k.stdoutbuffMutex.Lock()
			k.stdoutbuff.Write(stdoutBytes[:stdoutBytesLen])
			k.stdoutbuffMutex.Unlock()

			str := base64.StdEncoding.EncodeToString(stdoutBytes[:stdoutBytesLen])

			k.sendStreamMessage(smsg.ShellStdOut, str)

			// Wait for stdout to process more data
			time.Sleep(time.Millisecond)
		}
	}
}

func (k *UnixShell) sendStreamMessage(streamType smsg.StreamType, content string) {
	message := smsg.StreamMessage{
		Type:           string(streamType),
		SequenceNumber: k.streamSequenceNumber,
		Content:        content,
	}

	k.streamOutputChan <- message
	k.streamSequenceNumber++
}
