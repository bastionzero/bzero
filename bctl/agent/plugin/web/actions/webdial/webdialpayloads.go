package webdial

type WebDialActionPayload struct {
	RequestId string `json:"requestId"`
}

type WebDataInActionPayload struct {
	RequestId string `json:"requestId"`
	Content   string `json:"content"`
}
