package stream

import (
	"encoding/base64"
	"encoding/json"
	"flag"
	"os"
	"testing"
	"time"

	"bastionzero.com/bctl/v1/bzerolib/logger"
	"bastionzero.com/bctl/v1/bzerolib/plugin/kube/actions/stream"
	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
	"bastionzero.com/bctl/v1/bzerolib/tests"
	"github.com/stretchr/testify/assert"
	"gopkg.in/tomb.v2"
)

func TestMain(m *testing.M) {
	flag.Parse()

	exitCode := m.Run()

	// Exit
	os.Exit(exitCode)
}

func TestStream(t *testing.T) {
	assert := assert.New(t)
	var tmb tomb.Tomb
	logger := logger.MockLogger()
	requestId := "rid"
	logId := "lid"
	command := "logs"
	sendData := "send data"
	receiveData1 := "receive data"
	receiveData2 := "receive data 2"
	receiveData4 := "receive data 4"
	urlPath := "test-path"

	request := tests.MockHttpRequest("GET", urlPath, make(map[string][]string), sendData)

	writer := tests.MockResponseWriter{}
	writer.On("Write", []byte(receiveData1)).Return(12, nil)
	writer.On("Write", []byte(receiveData2)).Return(14, nil)
	writer.On("Header").Return(make(map[string][]string))

	t.Logf("Test that we can create a new Stream action")
	s, outputChan := New(logger, requestId, logId, command)

	go func() {
		startMessage := <-outputChan

		t.Logf("Test that it sends a Stream payload to the agent")
		assert.Equal(string(stream.StreamStart), startMessage.Action)
		var payload stream.KubeStreamActionPayload
		err := json.Unmarshal(startMessage.ActionPayload, &payload)
		assert.Nil(err)
		assert.Equal(sendData, payload.Body)
		assert.Equal(command, payload.CommandBeingRun)
		assert.Equal(requestId, payload.RequestId)
		assert.Equal(logId, payload.LogId)

		t.Logf("Test that it will not process a stream received before the header message")
		// send early streams (msgs 1 and 4)
		message1 := smsg.StreamMessage{
			Type:           smsg.Data,
			SequenceNumber: 1,
			More:           true,
			Content:        base64.StdEncoding.EncodeToString([]byte(receiveData1)),
		}
		s.ReceiveStream(message1)
		message4 := smsg.StreamMessage{
			Type:           smsg.Data,
			SequenceNumber: 4,
			More:           true,
			Content:        base64.StdEncoding.EncodeToString([]byte(receiveData4)),
		}

		// this functions as an implicit assertion because there is no On() call accounting for it
		// so if the message were processed, we'd panic
		t.Logf("Test that it will not process a stream with an out-of-order sequence number if it never reaches that number")
		s.ReceiveStream(message4)

		// send header message
		kubeWatchHeadersPayload := stream.KubeStreamHeadersPayload{
			Headers: map[string][]string{
				"Content-Length": {"12"},
				"Origin":         {"val1", "val2"},
			},
		}
		kubeWatchHeadersPayloadBytes, err := json.Marshal(kubeWatchHeadersPayload)
		assert.Nil(err)

		message0 := smsg.StreamMessage{
			Type:           smsg.Data,
			SequenceNumber: 0,
			Content:        base64.StdEncoding.EncodeToString(kubeWatchHeadersPayloadBytes),
		}
		// upon receiving this, message 1 will also be processed
		s.ReceiveStream(message0)

		t.Logf("Test that we can end the stream listener")
		message2 := smsg.StreamMessage{
			Type:           smsg.Data,
			SequenceNumber: 2,
			More:           true,
			Content:        base64.StdEncoding.EncodeToString([]byte(receiveData2)),
		}

		t.Logf("Test that it processes in-order messages immediately")
		s.ReceiveStream(message2)
		// send this again -- still shouldn't be processed
		s.ReceiveStream(message4)

		t.Logf("Test that we can end the stream listener")
		// send a stream end message
		endMessage := smsg.StreamMessage{
			Type:           smsg.Data,
			SequenceNumber: 5,
			More:           false,
			Content:        base64.StdEncoding.EncodeToString([]byte("the end")),
		}
		s.ReceiveStream(endMessage)

		time.Sleep(1 * time.Second)
		writer.AssertExpectations(t)
	}()

	t.Logf("Test that we can start the action")
	err := s.Start(&tmb, &writer, &request)
	assert.Nil(err)
}

// TODO: could have a test that ends via Context().Done() or a tomb kill
// but both of those also return a nil error
