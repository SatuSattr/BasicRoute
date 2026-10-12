package model




type ModelObject struct {
	ID           string  `json:"id"`
	Object       string  `json:"object"` 
	Created      int64   `json:"created,omitempty"`
	OwnedBy      string  `json:"owned_by"`
	ShutdownDate *string `json:"shutdown_date,omitempty"`
}


type ModelsListResponse struct {
	Object string        `json:"object"` 
	Data   []ModelObject `json:"data"`
}




type ErrorDetail struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Param   any    `json:"param"`
	Code    any    `json:"code"`
}


type ErrorResponse struct {
	Error ErrorDetail `json:"error"`
}



func NewError(msg, typ, code string) ErrorResponse {
	var c any
	if code != "" {
		c = code
	}
	return ErrorResponse{Error: ErrorDetail{
		Message: msg,
		Type:    typ,
		Param:   nil,
		Code:    c,
	}}
}
