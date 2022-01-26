package webdial

type WebDialActionPayload struct {
	RequestId string `json:"requestId"`
}

type WebDataInActionPayload struct {
	Body           string              `json:"body"`
	Endpoint       string              `json:"endpoint"`
	Headers        map[string][]string `json:"headers"`
	Method         string              `json:"method"`
	SequenceNumber int                 `json:"sequenceNumber"`
	RequestId      string              `json:"requestId"`
}
