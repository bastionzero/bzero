package stream

import (
	"encoding/base64"
	"encoding/json"
	"flag"
	"os"
	"testing"
	"time"

	"bastionzero.com/bctl/v1/bzerolib/plugin/kube/actions/stream"
	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
	"bastionzero.com/bctl/v1/bzerolib/testutils"
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
	logger := testutils.MockLogger()
	requestId := "rid"
	logId := "lid"
	command := "logs"
	sendData := "send data"
	receiveData1 := "receive data"
	receiveData2 := "receive data 2"
	receiveData4 := "receive data 4"
	urlPath := "test-path"
	// TODO: check validity
	r, outputChan := New(logger, requestId, logId, command)

	request := testutils.MockHttpRequest("GET", urlPath, make(map[string][]string), sendData)

	writer := testutils.MockResponseWriter{}
	writer.On("Write", []byte(receiveData1)).Return(12, nil)
	writer.On("Write", []byte(receiveData2)).Return(14, nil)
	writer.On("Header").Return(make(map[string][]string))

	go func() {
		// get initial payload from output channel
		reqMessage := <-outputChan
		assert.Equal(string(stream.StreamStart), reqMessage.Action)
		var payload stream.KubeStreamActionPayload
		err := json.Unmarshal(reqMessage.ActionPayload, &payload)
		assert.Nil(err)
		assert.Equal(sendData, payload.Body)
		assert.Equal(command, payload.CommandBeingRun)
		assert.Equal(requestId, payload.RequestId)
		assert.Equal(logId, payload.LogId)

		// send early streams (msgs 1 and 4)
		message1 := smsg.StreamMessage{
			Type:           smsg.Data,
			SequenceNumber: 1,
			More:           true,
			Content:        base64.StdEncoding.EncodeToString([]byte(receiveData1)),
		}
		r.ReceiveStream(message1)
		message4 := smsg.StreamMessage{
			Type:           smsg.Data,
			SequenceNumber: 4,
			More:           true,
			Content:        base64.StdEncoding.EncodeToString([]byte(receiveData4)),
		}
		// implicit assertion that this call never happens
		r.ReceiveStream(message4)

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
		r.ReceiveStream(message0)

		message2 := smsg.StreamMessage{
			Type:           smsg.Data,
			SequenceNumber: 2,
			More:           true,
			Content:        base64.StdEncoding.EncodeToString([]byte(receiveData2)),
		}
		// this should be processed immediately
		r.ReceiveStream(message2)
		// send this again -- still shouldn't be processed
		r.ReceiveStream(message4)

		// send a stream end message
		endMessage := smsg.StreamMessage{
			Type:           smsg.Data,
			SequenceNumber: 5,
			More:           false,
			Content:        base64.StdEncoding.EncodeToString([]byte("the end")),
		}
		r.ReceiveStream(endMessage)

		// OTHER WAYS TO END THIS THOUGH

		// FIXME: address race condition here
		time.Sleep(1 * time.Second)
		writer.AssertExpectations(t)
	}()

	err := r.Start(&tmb, &writer, &request)
	assert.Nil(err)
}

// TODO: could have a test that ends via Context().Done() or a tomb kill
// but both of those also return a nil error
