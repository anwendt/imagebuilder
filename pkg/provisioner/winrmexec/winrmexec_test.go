package winrmexec_test

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anwendt/imagebuilder/pkg/provisioner/winrmexec"
)

func TestClient_ExecutePowerShell_SendsWinRMSequence(t *testing.T) {
	workspace := t.TempDir()
	passwordPath := filepath.Join(workspace, "password")
	if err := os.WriteFile(passwordPath, []byte("secret\n"), 0o600); err != nil {
		t.Fatalf("write password: %v", err)
	}
	var actions []string
	receiveCount := 0
	client := winrmexec.Client{HTTPClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		actions = append(actions, req.Header.Get("SOAPAction"))
		body := responseForAction(req.Header.Get("SOAPAction"), &receiveCount)
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       ioNopCloser(body),
			Header:     http.Header{},
			Request:    req,
		}, nil
	})}}

	err := client.ExecutePowerShell(context.Background(), winrmexec.Access{
		EndpointURL:  "http://127.0.0.1:5985/wsman",
		User:         "Administrator",
		PasswordPath: passwordPath,
	}, "Write-Host ok")
	if err != nil {
		t.Fatalf("ExecutePowerShell returned error: %v", err)
	}
	if len(actions) != 5 {
		t.Fatalf("actions = %#v", actions)
	}
	if !strings.Contains(actions[1], "Command") || !strings.Contains(actions[2], "Receive") {
		t.Fatalf("actions = %#v", actions)
	}
}

func responseForAction(action string, receiveCount *int) string {
	switch {
	case strings.Contains(action, "Create"):
		return `<s:Envelope><s:Body><rsp:ShellId>shell-1</rsp:ShellId></s:Body></s:Envelope>`
	case strings.Contains(action, "Command"):
		return `<s:Envelope><s:Body><rsp:CommandId>command-1</rsp:CommandId></s:Body></s:Envelope>`
	case strings.Contains(action, "Receive"):
		*receiveCount++
		if *receiveCount == 1 {
			return `<s:Envelope><s:Body><rsp:Stream Name="stdout">c3RpbGwgcnVubmluZw==</rsp:Stream></s:Body></s:Envelope>`
		}
		return `<s:Envelope><s:Body><rsp:ExitCode>0</rsp:ExitCode></s:Body></s:Envelope>`
	default:
		return `<s:Envelope><s:Body/></s:Envelope>`
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type stringReadCloser struct {
	*strings.Reader
}

func ioNopCloser(value string) stringReadCloser {
	return stringReadCloser{Reader: strings.NewReader(value)}
}

func (c stringReadCloser) Close() error { return nil }
