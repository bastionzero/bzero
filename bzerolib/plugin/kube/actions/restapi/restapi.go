package restapi

type RestSubAction string

const (
	RestResponse RestSubAction = "kube/restapi/response"
	RestRequest  RestSubAction = "kube/restapi/request"
)
