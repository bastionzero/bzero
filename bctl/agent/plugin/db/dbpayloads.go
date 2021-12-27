package db

// For "db/..." actions

type DbDataInActionPayload struct {
	Data           string `json:"data"`
	SequenceNumber int    `json:"sequenceNumber"`
}
