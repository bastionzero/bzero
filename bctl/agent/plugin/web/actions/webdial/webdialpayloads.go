package webdial

type WebDialActionPayload struct {
	RequestId string `json:"requestId"`
}

type WebDataInActionPayload struct {
	Body           []byte              `json:"body"`
	Endpoint       string              `json:"endpoint"`
	Headers        map[string][]string `json:"headers"`
	Method         string              `json:"method"`
	SequenceNumber int                 `json:"sequenceNumber"`
	RequestId      string              `json:"requestId"`
}

type WebDataOutActionPayload struct {
	StatusCode int                 `json:"statusCode"`
	RequestId  string              `json:"requestId"`
	Headers    map[string][]string `json:"headers"`
	Content    []byte              `json:"content"`
}
