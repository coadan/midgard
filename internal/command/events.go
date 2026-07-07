package command

import "encoding/json"

type Event struct {
	Type    string
	Payload string
}

func Events(result Result) ([]Event, error) {
	payload, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	return []Event{
		{Type: "command.finished", Payload: string(payload)},
	}, nil
}
