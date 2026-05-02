package builder

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

type BootEventType string

const (
	BootEventKey  BootEventType = "key"
	BootEventText BootEventType = "text"
	BootEventWait BootEventType = "wait"
)

type BootEvent struct {
	Type  BootEventType
	Value string
	Wait  time.Duration
}

var bootTokenKeys = map[string]string{
	"enter":  "enter",
	"tab":    "tab",
	"esc":    "esc",
	"up":     "up",
	"down":   "down",
	"left":   "left",
	"right":  "right",
	"ctrl-x": "ctrl-x",
}

func ParseBootCommand(commands []string) ([]BootEvent, error) {
	var events []BootEvent
	for _, command := range commands {
		for len(command) > 0 {
			start := strings.Index(command, "<")
			if start < 0 {
				events = appendTextEvent(events, command)
				break
			}
			if start > 0 {
				events = appendTextEvent(events, command[:start])
				command = command[start:]
				continue
			}
			end := strings.Index(command, ">")
			if end < 0 {
				return nil, fmt.Errorf("unterminated boot command token in %q", command)
			}
			token := strings.ToLower(command[1:end])
			event, err := bootTokenEvent(token)
			if err != nil {
				return nil, err
			}
			events = append(events, event)
			command = command[end+1:]
		}
	}
	return events, nil
}

func appendTextEvent(events []BootEvent, text string) []BootEvent {
	if text == "" {
		return events
	}
	return append(events, BootEvent{Type: BootEventText, Value: text})
}

func bootTokenEvent(token string) (BootEvent, error) {
	if token == "wait" {
		return BootEvent{Type: BootEventWait, Wait: time.Second}, nil
	}
	if strings.HasPrefix(token, "wait") {
		seconds, err := strconv.Atoi(strings.TrimPrefix(token, "wait"))
		if err != nil || seconds <= 0 {
			return BootEvent{}, fmt.Errorf("invalid wait token <%s>", token)
		}
		return BootEvent{Type: BootEventWait, Wait: time.Duration(seconds) * time.Second}, nil
	}
	if strings.HasPrefix(token, "f") {
		n, err := strconv.Atoi(strings.TrimPrefix(token, "f"))
		if err == nil && n >= 1 && n <= 12 {
			return BootEvent{Type: BootEventKey, Value: token}, nil
		}
	}
	key, ok := bootTokenKeys[token]
	if !ok {
		return BootEvent{}, fmt.Errorf("unsupported boot command token <%s>", token)
	}
	return BootEvent{Type: BootEventKey, Value: key}, nil
}

func BootEventsToQEMUMonitorScript(events []BootEvent) []string {
	var lines []string
	for _, event := range events {
		switch event.Type {
		case BootEventKey:
			lines = append(lines, "sendkey "+qemuKeyName(event.Value))
		case BootEventText:
			for _, r := range event.Value {
				lines = append(lines, "sendkey "+qemuRuneKey(r))
			}
		case BootEventWait:
			lines = append(lines, fmt.Sprintf("sleep %d", int(event.Wait.Seconds())))
		}
	}
	return lines
}

func qemuKeyName(key string) string {
	switch key {
	case "enter":
		return "ret"
	case "esc":
		return "esc"
	case "ctrl-x":
		return "ctrl-x"
	default:
		return key
	}
}

func qemuRuneKey(r rune) string {
	switch r {
	case ' ':
		return "spc"
	case '-':
		return "minus"
	case '=':
		return "equal"
	case '/':
		return "slash"
	case ':':
		return "shift-semicolon"
	case '.':
		return "dot"
	default:
		return strings.ToLower(string(r))
	}
}
