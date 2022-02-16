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

package datachannel

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"bastionzero.com/bctl/v1/bctl/agent/keysplitting"
	am "bastionzero.com/bctl/v1/bzerolib/channels/agentmessage"
	"bastionzero.com/bctl/v1/bzerolib/channels/websocket"
	"bastionzero.com/bctl/v1/bzerolib/keysplitting/bzcert"
	ksmsg "bastionzero.com/bctl/v1/bzerolib/keysplitting/message"
	bzshell "bastionzero.com/bctl/v1/bzerolib/plugin/shell"
	"bastionzero.com/bctl/v1/bzerolib/testutils"

	"github.com/stretchr/testify/assert"
	"gopkg.in/tomb.v2"
)

type MocktKeysplitting struct{ keysplitting.Keysplitting }

func (m *MocktKeysplitting) GetHpointer() string                                 { return "fake" }
func (m *MocktKeysplitting) Validate(ksMessage *ksmsg.KeysplittingMessage) error { return nil }
func (m *MocktKeysplitting) BuildResponse(ksMessage *ksmsg.KeysplittingMessage,
	action string,
	actionPayload []byte) (ksmsg.KeysplittingMessage, error) {

	var responseMessage ksmsg.KeysplittingMessage
	switch ksMessage.Type {
	case ksmsg.Syn:
		responseMessage = ksmsg.KeysplittingMessage{
			Type:                ksmsg.SynAck,
			KeysplittingPayload: ksmsg.SynAckPayload{},
		}
	case ksmsg.Data:
		responseMessage = ksmsg.KeysplittingMessage{
			Type:                ksmsg.DataAck,
			KeysplittingPayload: ksmsg.DataAckPayload{},
		}
	}
	return responseMessage, nil
}

type TestWebsocket struct {
	websocket.Websocket
	MsgsSent []am.AgentMessage
}

func (w *TestWebsocket) Connect() {}
func (w *TestWebsocket) Send(agentMessage am.AgentMessage) {
	w.MsgsSent = append(w.MsgsSent, agentMessage)
}
func (w *TestWebsocket) Unsubscribe(id string)                           {}
func (w *TestWebsocket) Subscribe(id string, channel websocket.IChannel) {}
func (w *TestWebsocket) Close(err error)                                 {}

func CreateSynMsg(t *testing.T) []byte {
	fakebzcert := bzcert.BZCert{}
	runAsUser := testutils.GetRunAsUser(t)
	synActionPayload, _ := json.Marshal(bzshell.ShellOpenMessage{RunAsUser: runAsUser})

	synPayload := ksmsg.SynPayload{
		Timestamp:     fmt.Sprint(time.Now().Unix()),
		SchemaVersion: ksmsg.SchemaVersion,
		Type:          string(ksmsg.Syn),
		Action:        "shell/open",
		ActionPayload: synActionPayload,
		TargetId:      "currently unused",
		Nonce:         "fake nonce",
		BZCert:        fakebzcert,
	}

	// var synPayload ksmsg.KeysplittingMessage
	synBytes, _ := json.Marshal(ksmsg.KeysplittingMessage{
		Type:                ksmsg.Syn,
		KeysplittingPayload: synPayload,
		Signature:           "fake signature",
	})

	return synBytes
}

func CreateOpenShellDataMsg() []byte {
	dataActionPayload, _ := json.Marshal(bzshell.ShellOpenMessage{RunAsUser: "test-user"})

	dataPayload := ksmsg.DataPayload{
		Timestamp:     fmt.Sprint(time.Now().Unix()),
		SchemaVersion: ksmsg.SchemaVersion,
		Type:          string(ksmsg.Data),
		Action:        "shell/open",
		ActionPayload: testutils.B64Encode(dataActionPayload),
		TargetId:      "currently unused",
		BZCertHash:    "fake",
	}

	dataBytes, _ := json.Marshal(ksmsg.KeysplittingMessage{
		Type:                ksmsg.Data,
		KeysplittingPayload: dataPayload,
		Signature:           "fake signature",
	})

	return dataBytes
}

func CreateAgentMessage(dataBytes []byte) am.AgentMessage {
	agentMsg := am.AgentMessage{
		ChannelId:      "fake",
		MessageType:    "keysplitting",
		SchemaVersion:  ksmsg.SchemaVersion,
		MessagePayload: dataBytes,
	}
	return agentMsg
}

func TestShelllDatachannel(t *testing.T) {

	assert.Contains(t, string("abce"), "ab")

	var tmb tomb.Tomb
	subLogger := testutils.MockLogger()
	ws := &TestWebsocket{}
	dcID := "testID-1"

	synBytes := CreateSynMsg(t)
	datachannel, err := New(&tmb, subLogger, ws, dcID, synBytes, &MocktKeysplitting{})

	assert.NotNil(t, datachannel)
	assert.Nil(t, err)

	assert.Equal(t, len(ws.MsgsSent), 1)
	assert.EqualValues(t, "keysplitting", ws.MsgsSent[0].MessageType)

	var synackMsg ksmsg.KeysplittingMessage
	err = json.Unmarshal(ws.MsgsSent[0].MessagePayload, &synackMsg)
	assert.Nil(t, err)
	assert.EqualValues(t, ksmsg.SynAck, synackMsg.Type)

	dataBytes := CreateOpenShellDataMsg()
	agentMsg := CreateAgentMessage(dataBytes)

	datachannel.processInput(agentMsg)

	assert.Equal(t, len(ws.MsgsSent), 2)
	assert.EqualValues(t, "keysplitting", ws.MsgsSent[1].MessageType)

	var dataAckMsg ksmsg.KeysplittingMessage
	err = json.Unmarshal(ws.MsgsSent[1].MessagePayload, &dataAckMsg)
	assert.Nil(t, err)
	assert.EqualValues(t, ksmsg.DataAck, dataAckMsg.Type)

}
