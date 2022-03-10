package webdial

type WebDialSubAction string

const (
	WebDialStart     WebDialSubAction = "web/dial/start"
	WebDialInput     WebDialSubAction = "web/dial/datain"
	WebDialInterrupt WebDialSubAction = "web/dial/interrupt"
)

type WebDialActionPayload struct {
	RequestId string `json:"requestId"`
}

type WebInputActionPayload struct {
	Body           []byte              `json:"body"`
	Endpoint       string              `json:"endpoint"`
	Headers        map[string][]string `json:"headers"`
	Method         string              `json:"method"`
	SequenceNumber int                 `json:"sequenceNumber"`
	RequestId      string              `json:"requestId"`
}

type WebOutputActionPayload struct {
	StatusCode int                 `json:"statusCode"`
	RequestId  string              `json:"requestId"`
	Headers    map[string][]string `json:"headers"`
	Content    []byte              `json:"content"`
}

type WebInterruptActionPayload struct {
	RequestId string `json:"requestId"`
}
