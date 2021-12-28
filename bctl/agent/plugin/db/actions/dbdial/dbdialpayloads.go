package dbdial

type DbDialActionPayload struct {
	RequestId string `json:"requestId"`
}

type DbDataInActionPayload struct {
	Data           string `json:"data"`
	SequenceNumber int    `json:"sequenceNumber"`
	RequestId      string `json:"requestId"`
}
