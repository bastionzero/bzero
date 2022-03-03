// Copyright 2022 BastionZero Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License"). You may not
// use this file except in compliance with the License. A copy of the
// License is located at
//
// https://www.apache.org/licenses/LICENSE-2.0
//
// or in the "license" file accompanying this file. This file is distributed
// on an "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND,
// either express or implied. See the License for the specific language governing
// permissions and limitations under the License.

package shell

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"testing"

	bzshell "bastionzero.com/bctl/v1/bzerolib/plugin/shell"
	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
	"bastionzero.com/bctl/v1/bzerolib/testutils"
	"github.com/stretchr/testify/assert"
	"gopkg.in/tomb.v2"
)

func StreamMessageToString(t *testing.T, msg smsg.StreamMessage) string {
	msgbyte, err := base64.StdEncoding.DecodeString(string(msg.Content))
	if err != nil {
		t.Errorf("Shell stdout stream base64 decode failed: %v", err)
	}
	return string(msgbyte)
}

func SpawnTerminal(t *testing.T, streamOutputChan chan smsg.StreamMessage) *ShellPlugin {
	subLogger := testutils.MockLogger().GetPluginLogger(string("unittest shell"))
	testshelluser := testutils.GetRunAsUser(t)

	var tmb tomb.Tomb
	synPayload, _ := json.Marshal(bzshell.ShellOpenMessage{
		TargetUser: testshelluser,
	})
	plugin, err := New(&tmb, subLogger, streamOutputChan, synPayload)
	if err != nil {
		t.Errorf("Shell plugin new failed: %v", err.Error())
	}
	if plugin == nil {
		t.Errorf("Plugin is nil")
	}
	var action = "shell/open"

	openPayload, _ := json.Marshal(bzshell.ShellOpenMessage{})

	respstr, respbytes, err := plugin.Receive(action, openPayload)

	if err != nil {
		t.Errorf("Shell start threw error: %v", err)
	}

	assert.NotNil(t, respbytes)
	assert.NotEmpty(t, respstr)

	time.Sleep(1 * time.Second)

	return plugin
}

func SendResize(t *testing.T, plugin *ShellPlugin, rows uint32, cols uint32) {
	action := "shell/resize"

	resizePayload, _ := json.Marshal(bzshell.ShellResizeMessage{
		Rows: rows,
		Cols: cols,
	})

	respstr, respbytes, err := plugin.Receive(action, resizePayload)
	if err != nil {
		t.Errorf("Shell input threw error: %v", err)
	}

	assert.NotNil(t, respbytes)
	assert.NotEmpty(t, respstr)
}

func SendReplay(t *testing.T, plugin *ShellPlugin) []byte {
	action := "shell/replay"

	replayPayload, _ := json.Marshal(bzshell.ShellReplayMessage{})

	respstr, respbytes, err := plugin.Receive(action, replayPayload)
	if err != nil {
		t.Errorf("Shell input threw error: %v", err)
	}

	assert.NotNil(t, respbytes)
	assert.NotEmpty(t, respstr)

	return respbytes
}

func SendClose(t *testing.T, plugin *ShellPlugin) {
	action := "shell/close"

	closePayload, _ := json.Marshal(bzshell.ShellCloseMessage{})

	respstr, respbytes, err := plugin.Receive(action, closePayload)
	if err != nil {
		t.Errorf("Shell input threw error: %v", err)
	}

	assert.NotNil(t, respbytes)
	assert.NotEmpty(t, respstr)
}

func SendToStdIn(t *testing.T, plugin *ShellPlugin, stdinstr string) {
	action := "shell/input"

	inputPayload, _ := json.Marshal(bzshell.ShellInputMessage{
		Data: base64.StdEncoding.EncodeToString([]byte(stdinstr)),
	})

	respstr, respbytes, err := plugin.Receive(action, inputPayload)
	if err != nil {
		t.Errorf("Shell input threw error: %v", err)
	}

	assert.NotNil(t, respbytes)
	assert.NotEmpty(t, respstr)
}

// This function ensures that if the channel doesn't receive any output the test won't hang forever
//  TODO: I don't like this pattern. I should replace it write an anonymous function that writes to a buffer
func ReadOutputOrTimeout(t *testing.T, ch chan smsg.StreamMessage) (string, error) {
	select {
	case msg := <-ch:
		msgstr := StreamMessageToString(t, msg)
		return msgstr, nil
	case <-time.After(3000 * time.Millisecond):
		t.Errorf("Output Channel read timeout")
		return "", fmt.Errorf("Channel read timedout")
	}
}

func TestInputOutput(t *testing.T) {
	streamOutputChan := make(chan smsg.StreamMessage, 20)

	plugin := SpawnTerminal(t, streamOutputChan)
	assert.NotNil(t, plugin)

	outstr, err := ReadOutputOrTimeout(t, streamOutputChan)
	assert.Nil(t, err)

	t.Logf("Terminal says: %v", outstr)
	assert.Contains(t, outstr, "sh")

	lscmd := "ls -l\n"
	SendToStdIn(t, plugin, lscmd)

	outstr, err = ReadOutputOrTimeout(t, streamOutputChan)
	assert.Nil(t, err)
	assert.EqualValues(t, strings.TrimSpace(lscmd), strings.TrimSpace(outstr)) // Shell should always reflect back the entered command

	outstr, err = ReadOutputOrTimeout(t, streamOutputChan)
	assert.Nil(t, err)
	assert.Contains(t, outstr, "total") // ls -l always returns total as the first line
}

func TestShelllReplay(t *testing.T) {

	streamOutputChan := make(chan smsg.StreamMessage, 20)

	plugin := SpawnTerminal(t, streamOutputChan)
	stdoutreplay := SendReplay(t, plugin)

	assert.NotNil(t, stdoutreplay)
	assert.Equal(t, 16, len(stdoutreplay))
	assert.Contains(t, string(stdoutreplay), "sh")

	stdoutreplay = SendReplay(t, plugin)
	assert.NotNil(t, stdoutreplay)
	assert.Equal(t, 16, len(stdoutreplay))
	assert.Contains(t, string(stdoutreplay), "sh")

	SendToStdIn(t, plugin, "echo 'abcdeabcdeabcdeabcdeabcdeabcde'")
	time.Sleep(100 * time.Millisecond)

	stdoutreplay = SendReplay(t, plugin)
	assert.NotNil(t, stdoutreplay)
	assert.Equal(t, 53, len(stdoutreplay))
	assert.Contains(t, string(stdoutreplay), "sh")
	assert.Contains(t, string(stdoutreplay), "abcdeabcdeabcdeabcdeabcdeabcde")
}

func TestResize(t *testing.T) {

	streamOutputChan := make(chan smsg.StreamMessage, 20)

	plugin := SpawnTerminal(t, streamOutputChan)
	assert.NotNil(t, plugin)
	outstr, err := ReadOutputOrTimeout(t, streamOutputChan)
	assert.Nil(t, err)

	assert.Contains(t, outstr, "sh")

	rows := uint32(23)
	cols := uint32(5)
	SendResize(t, plugin, rows, cols)
	// This test checks that Shell/Resize doesn't throw an error, it does not confirm that the terminal was resized correctly
}

func TestClose(t *testing.T) {
	streamOutputChan := make(chan smsg.StreamMessage, 20)

	plugin := SpawnTerminal(t, streamOutputChan)
	assert.NotNil(t, plugin)
	outstr, err := ReadOutputOrTimeout(t, streamOutputChan)
	assert.Nil(t, err)

	assert.Contains(t, outstr, "sh")

	SendClose(t, plugin)

	action := "shell/input"
	b64instr := base64.StdEncoding.EncodeToString([]byte("ls -l\n"))
	inputPayload, _ := json.Marshal(bzshell.ShellInputMessage{
		Data: b64instr,
	})

	// Throw an error because the shell is now closed
	_, _, err = plugin.Receive(action, inputPayload)
	if err == nil {
		t.Errorf("Error expected. shell/close should cause shell/input to fail")
	}
}

func TestNoUserExistsErr(t *testing.T) {

	streamOutputChan := make(chan smsg.StreamMessage, 20)

	subLogger := testutils.MockLogger().GetPluginLogger(string("unittest shell"))
	var tmb tomb.Tomb

	userThatDoesNotExist := "NoSuchUser"

	synPayload, _ := json.Marshal(bzshell.ShellOpenMessage{
		TargetUser: userThatDoesNotExist,
	})
	plugin, err := New(&tmb, subLogger, streamOutputChan, synPayload)
	if err != nil {
		t.Errorf("Shell plugin new failed: %v", err.Error())
	}
	if plugin == nil {
		t.Errorf("Plugin is nil")
	}
	var action = "shell/open"

	openPayload, _ := json.Marshal(bzshell.ShellOpenMessage{})
	b64payload := testutils.B64Encode(openPayload)

	_, _, err = plugin.Receive(action, b64payload)

	assert.EqualError(t, err, "Unable to start shell: failed to start pty since RunAs user NoSuchUser does not exist")
}
