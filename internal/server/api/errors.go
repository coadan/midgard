package api

type badRequest string

func (e badRequest) Error() string {
	return string(e)
}

func errBadRequest(message string) error {
	return badRequest(message)
}
