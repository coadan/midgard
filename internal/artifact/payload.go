package artifact

import "fmt"

var allowedPayloadTypes = map[string]bool{
	"script": true,
	"patch":  true,
	"file":   true,
	"json":   true,
	"text":   true,
}

func ValidatePayloadType(payloadType string) error {
	if !allowedPayloadTypes[payloadType] {
		return fmt.Errorf("invalid payload type %q", payloadType)
	}
	return nil
}
