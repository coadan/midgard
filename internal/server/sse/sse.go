package sse

import (
	"fmt"
	"net/http"
	"strings"
)

type Event struct {
	ID   string
	Type string
	Data string
}

func Prepare(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
}

func Write(w http.ResponseWriter, event Event) error {
	if event.ID != "" {
		if _, err := fmt.Fprintf(w, "id: %s\n", sanitizeLine(event.ID)); err != nil {
			return err
		}
	}
	if event.Type != "" {
		if _, err := fmt.Fprintf(w, "event: %s\n", sanitizeLine(event.Type)); err != nil {
			return err
		}
	}
	for _, line := range strings.Split(event.Data, "\n") {
		if _, err := fmt.Fprintf(w, "data: %s\n", strings.TrimRight(line, "\r")); err != nil {
			return err
		}
	}
	_, err := fmt.Fprint(w, "\n")
	return err
}

func sanitizeLine(value string) string {
	value = strings.ReplaceAll(value, "\n", " ")
	value = strings.ReplaceAll(value, "\r", " ")
	return value
}
