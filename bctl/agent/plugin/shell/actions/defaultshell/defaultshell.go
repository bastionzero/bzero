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

package defaultshell

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"bastionzero.com/bctl/v1/bctl/agent/config"
	"bastionzero.com/bctl/v1/bctl/agent/plugin/shell/actions/defaultshell/pseudoterminal"

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
type DefaultShell struct {
	tmb    *tomb.Tomb // datachannel's tomb
	logger *logger.Logger

	streamOutputChan     chan smsg.StreamMessage
	runAsUser            string
	streamSequenceNumber int

	// standard out buffer
	stdoutbuff      *ringbuffer.RingBuffer
	stdoutbuffMutex sync.Mutex

	// interface for interacting with pty
	terminal pseudoterminal.IPseudoTerminal
}

// New returns a new instance of the DefaultShell
func New(
	parentTmb *tomb.Tomb,
	logger *logger.Logger,
	ch chan smsg.StreamMessage,
	runAsUser string) (*DefaultShell, error) {
	return &DefaultShell{
		runAsUser:            runAsUser,
		logger:               logger,
		tmb:                  parentTmb, // if datachannel dies, so should we
		streamOutputChan:     ch,
		streamSequenceNumber: 1,
	}, nil
}

// Receive takes input from a client using the MRZAP datachannel and returns output via the MRZAP datachannel
func (d *DefaultShell) Receive(action string, actionPayload []byte) (string, []byte, error) {
	d.logger.Infof("Plugin received Data message with %v action", action)

	switch bzshell.ShellSubAction(action) {
	case bzshell.ShellOpen:
		// We ignore the RunAsUser in the Shell/Open message since it was set by
		// the plugin when it processes the SYN message. This is important
		// because the RunAsUser is part of policy and the policy check happens
		// based on the SYN message when the datachannel is first opened.
		if err := d.open(); err != nil {
			errorString := fmt.Errorf("unable to start shell: %s", err)
			d.logger.Error(errorString)
			return "", []byte{}, errorString
		}
	case bzshell.ShellClose:
		d.terminal.Kill()
	case bzshell.ShellInput:
		var shellInput bzshell.ShellInputMessage

		if err := json.Unmarshal(actionPayload, &shellInput); err != nil {
			rerr := fmt.Errorf("malformed shell input payload %v: %s", actionPayload, err)
			d.logger.Error(rerr)
			return action, []byte{}, rerr
		}

		if err := d.writeToTerminal(shellInput); err != nil {
			rerr := fmt.Errorf("write to stdin failed %v", err)
			d.logger.Error(rerr)
			return action, []byte{}, rerr
		}
	case bzshell.ShellResize:
		var shellResize bzshell.ShellResizeMessage

		if err := json.Unmarshal(actionPayload, &shellResize); err != nil {
			rerr := fmt.Errorf("malformed shell resize payload %v", actionPayload)
			d.logger.Error(rerr)
			return action, []byte{}, rerr
		}

		if err := d.setSize(shellResize.Cols, shellResize.Rows); err != nil {
			rerr := fmt.Errorf("shell resize failed %v", err)
			d.logger.Error(rerr)
			return action, []byte{}, rerr
		}
	case bzshell.ShellReplay:
		var shellReplay bzshell.ShellReplayMessage

		if err := json.Unmarshal(actionPayload, &shellReplay); err != nil {
			rerr := fmt.Errorf("malformed shell replay output payload %v", actionPayload)
			d.logger.Error(rerr)
			return action, []byte{}, rerr
		}

		outbuff := make([]byte, config.ShellStdOutBuffCapacity)
		d.stdoutbuffMutex.Lock()
		defer d.stdoutbuffMutex.Unlock()

		// does this need a timeout?
		if n, err := d.stdoutbuff.Read(outbuff); err != nil {
			return action, []byte{}, fmt.Errorf("failed to read from stdout buff for shell replay %v", err)
		} else {
			return action, outbuff[0:n], nil
		}

	default:
		return action, []byte{}, fmt.Errorf("unrecognized shell action received: %s", action)
	}

	return action, []byte{}, nil
}

func (d *DefaultShell) open() error {
	// If this method "open" is called twice is means something has gone very
	//  wrong and failing early is the safest action.
	if d.terminal != nil {
		return fmt.Errorf("attempted to start the shell but a call to open a shell has already been made")
	}

	if terminal, err := pseudoterminal.Start(d.logger, d.runAsUser, ""); err != nil {
		return err
	} else {
		d.terminal = terminal
	}

	go func() {
		select {
		case <-d.tmb.Dying():
			d.terminal.Kill()
			return
		case <-d.terminal.Done():
			return
		}
	}()

	if err := d.writePump(); err != nil {
		d.logger.Error(err)
		d.terminal.Kill()
	}

	return nil
}

// writeToTerminal passes payload byte stream to shell stdin
func (d *DefaultShell) writeToTerminal(shellInput bzshell.ShellInputMessage) error {
	if d.terminal == nil {
		return fmt.Errorf("could not process input, no terminal associated with this action")
	} else if _, err := d.terminal.StdIn().Write(shellInput.Data); err != nil {
		d.logger.Errorf("Unable to write to stdin, err: %v.", err)
		return err
	} else {
		return nil
	}
}

// setSize resizes the pseudo-terminal pty
func (d *DefaultShell) setSize(cols, rows uint32) error {
	d.logger.Debugf("default shell received resize: {cols: %d, rows: %d}", cols, rows)
	if d.terminal == nil {
		return fmt.Errorf("can't set size of non-existant terminal")
	} else if err := d.terminal.SetSize(cols, rows); err != nil {
		d.logger.Errorf("Unable to set pty size: %s", err)
		return err
	} else {
		return nil
	}
}

// writePump reads from pty stdout and writes to datachannel.
func (d *DefaultShell) writePump() error {
	defer func() {
		if err := recover(); err != nil {
			d.logger.Errorf("WritePump thread crashed with message: %s", err)
			//logger.Errorf("Stacktrace:\n%s", debug.Stack())
		}
	}()

	d.stdoutbuff = ringbuffer.New(config.ShellStdOutBuffCapacity)

	stdoutBytes := make([]byte, config.StreamDataPayloadSize)
	reader := bufio.NewReader(d.terminal.StdOut())

	// Wait for all input commands to run.
	time.Sleep(time.Second)

	for {
		stdoutBytesLen, err := reader.Read(stdoutBytes)

		if err != nil {
			d.sendStreamMessage(smsg.Stop, "")
			return fmt.Errorf("WritePump failed when reading from stdout: %s", err)
		}

		d.stdoutbuffMutex.Lock()
		d.stdoutbuff.Write(stdoutBytes[:stdoutBytesLen])
		d.stdoutbuffMutex.Unlock()

		str := base64.StdEncoding.EncodeToString(stdoutBytes[:stdoutBytesLen])

		d.sendStreamMessage(smsg.StdOut, str)

		// Wait for stdout to process more data
		time.Sleep(time.Millisecond)
	}
}

func (d *DefaultShell) sendStreamMessage(streamType smsg.StreamType, content string) {
	message := smsg.StreamMessage{
		Type:           streamType,
		SequenceNumber: d.streamSequenceNumber,
		Content:        content,
	}

	d.streamOutputChan <- message
	d.streamSequenceNumber++
}
