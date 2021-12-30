package webdial

type WebDialActionPayload struct {
	RequestId string `json:"requestId"`
}

type WebDataInActionPayload struct {
	Data           string `json:"data"`
	SequenceNumber int    `json:"sequenceNumber"`
	RequestId      string `json:"requestId"`
}
