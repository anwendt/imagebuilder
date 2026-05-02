package builder

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"time"
)

const (
	defaultQMPDialTimeout = 30 * time.Second
	defaultQMPIOTimeout   = 10 * time.Second
)

type QMPClient interface {
	ExecuteHumanMonitorCommand(ctx context.Context, socketPath, command string) error
}

type QMPUnixClient struct {
	DialTimeout time.Duration
	IOTimeout   time.Duration
}

func (c QMPUnixClient) ExecuteHumanMonitorCommand(ctx context.Context, socketPath, command string) error {
	conn, err := c.dial(ctx, socketPath)
	if err != nil {
		return err
	}
	defer conn.Close()

	timeout := c.IOTimeout
	if timeout == 0 {
		timeout = defaultQMPIOTimeout
	}
	if err := conn.SetDeadline(time.Now().Add(timeout)); err != nil {
		return fmt.Errorf("set qmp deadline: %w", err)
	}

	decoder := json.NewDecoder(conn)
	encoder := json.NewEncoder(conn)
	if _, err := readQMPMessage(decoder); err != nil {
		return fmt.Errorf("read qmp greeting: %w", err)
	}
	if err := encoder.Encode(map[string]string{"execute": "qmp_capabilities"}); err != nil {
		return fmt.Errorf("send qmp capabilities: %w", err)
	}
	if err := readQMPReturn(decoder); err != nil {
		return fmt.Errorf("qmp capabilities: %w", err)
	}
	request := map[string]any{
		"execute": "human-monitor-command",
		"arguments": map[string]string{
			"command-line": command,
		},
	}
	if err := encoder.Encode(request); err != nil {
		return fmt.Errorf("send qmp monitor command %q: %w", command, err)
	}
	if err := readQMPReturn(decoder); err != nil {
		return fmt.Errorf("qmp monitor command %q: %w", command, err)
	}
	return nil
}

func (c QMPUnixClient) dial(ctx context.Context, socketPath string) (net.Conn, error) {
	timeout := c.DialTimeout
	if timeout == 0 {
		timeout = defaultQMPDialTimeout
	}
	dialCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	var lastErr error
	for {
		conn, err := (&net.Dialer{}).DialContext(dialCtx, "unix", socketPath)
		if err == nil {
			return conn, nil
		}
		lastErr = err
		select {
		case <-dialCtx.Done():
			return nil, fmt.Errorf("dial qmp socket %s: %w: %v", socketPath, dialCtx.Err(), lastErr)
		case <-ticker.C:
		}
	}
}

func readQMPReturn(decoder *json.Decoder) error {
	for {
		message, err := readQMPMessage(decoder)
		if err != nil {
			return err
		}
		if rawError, ok := message["error"]; ok {
			return fmt.Errorf("qmp error: %v", rawError)
		}
		if _, ok := message["return"]; ok {
			return nil
		}
	}
}

func readQMPMessage(decoder *json.Decoder) (map[string]any, error) {
	var message map[string]any
	if err := decoder.Decode(&message); err != nil {
		return nil, err
	}
	return message, nil
}
