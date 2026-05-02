package winrmexec

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

type Executor interface {
	ExecutePowerShell(ctx context.Context, access Access, script string) error
}

type Access struct {
	EndpointURL        string
	Host               string
	Port               int
	User               string
	PasswordPath       string
	HTTPS              bool
	InsecureSkipVerify bool
}

type Client struct {
	HTTPClient *http.Client
}

func (c Client) ExecutePowerShell(ctx context.Context, access Access, script string) error {
	password, err := readPassword(access.PasswordPath)
	if err != nil {
		return err
	}
	endpoint := access.EndpointURL
	if endpoint == "" {
		scheme := "http"
		if access.HTTPS {
			scheme = "https"
		}
		endpoint = fmt.Sprintf("%s://%s/wsman", scheme, net.JoinHostPort(access.Host, fmt.Sprintf("%d", access.Port)))
	}
	client := c.HTTPClient
	if client == nil {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		if access.HTTPS {
			transport.TLSClientConfig = &tls.Config{
				MinVersion:         tls.VersionTLS12,
				InsecureSkipVerify: access.InsecureSkipVerify, //nolint:gosec // User-selected for isolated self-signed WinRM builders.
			}
		}
		client = &http.Client{Timeout: 30 * time.Second, Transport: transport}
	}
	session := session{client: client, endpoint: endpoint, user: access.User, password: password}
	shellID, err := session.createShell(ctx)
	if err != nil {
		return err
	}
	defer session.deleteShell(context.WithoutCancel(ctx), shellID)
	commandID, err := session.runCommand(ctx, shellID, encodedCommand(script))
	if err != nil {
		return err
	}
	code, err := session.receive(ctx, shellID, commandID)
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("winrm command exited with code %d", code)
	}
	return nil
}

type session struct {
	client   *http.Client
	endpoint string
	user     string
	password string
}

func (s session) createShell(ctx context.Context) (string, error) {
	body := envelope(`<w:ResourceURI mustUnderstand="true">http://schemas.microsoft.com/wbem/wsman/1/windows/shell/cmd</w:ResourceURI><w:OperationTimeout>PT60S</w:OperationTimeout><rsp:Shell/>`)
	response, err := s.post(ctx, "http://schemas.xmlsoap.org/ws/2004/09/transfer/Create", body)
	if err != nil {
		return "", fmt.Errorf("create winrm shell: %w", err)
	}
	value := firstXMLValue(response, "ShellId")
	if value == "" {
		return "", fmt.Errorf("create winrm shell: missing ShellId")
	}
	return value, nil
}

func (s session) runCommand(ctx context.Context, shellID, command string) (string, error) {
	body := envelope(selector(shellID) + `<rsp:CommandLine><rsp:Command>` + xmlEscape(command) + `</rsp:Command></rsp:CommandLine>`)
	response, err := s.post(ctx, "http://schemas.microsoft.com/wbem/wsman/1/windows/shell/Command", body)
	if err != nil {
		return "", fmt.Errorf("run winrm command: %w", err)
	}
	value := firstXMLValue(response, "CommandId")
	if value == "" {
		return "", fmt.Errorf("run winrm command: missing CommandId")
	}
	return value, nil
}

func (s session) receive(ctx context.Context, shellID, commandID string) (int, error) {
	body := envelope(selector(shellID) + `<rsp:Receive><rsp:DesiredStream CommandId="` + xmlEscape(commandID) + `">stdout stderr</rsp:DesiredStream></rsp:Receive>`)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		response, err := s.post(ctx, "http://schemas.microsoft.com/wbem/wsman/1/windows/shell/Receive", body)
		if err != nil {
			return 0, fmt.Errorf("receive winrm command: %w", err)
		}
		if raw := firstXMLValue(response, "ExitCode"); raw != "" {
			var code int
			if _, err := fmt.Sscanf(raw, "%d", &code); err != nil {
				return 0, fmt.Errorf("parse winrm exit code %q: %w", raw, err)
			}
			return code, nil
		}
		if strings.Contains(string(response), "CommandState") && strings.Contains(string(response), "Done") {
			return 0, nil
		}
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-ticker.C:
		}
	}
}

func (s session) deleteShell(ctx context.Context, shellID string) {
	_, _ = s.post(ctx, "http://schemas.xmlsoap.org/ws/2004/09/transfer/Delete", envelope(selector(shellID)))
}

func (s session) post(ctx context.Context, action, body string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint, strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/soap+xml;charset=UTF-8")
	req.Header.Set("SOAPAction", action)
	req.SetBasicAuth(s.user, s.password)
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, string(bytes.TrimSpace(data)))
	}
	return data, nil
}

func readPassword(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("winrm password path is required")
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("stat winrm password path: %w", err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("winrm password path must be a file")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("winrm password file permissions must not grant group or other access")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read winrm password: %w", err)
	}
	password := strings.TrimRight(string(data), "\r\n")
	if password == "" {
		return "", fmt.Errorf("winrm password file is empty")
	}
	return password, nil
}

func encodedCommand(script string) string {
	return "powershell.exe -NoProfile -NonInteractive -ExecutionPolicy Bypass -EncodedCommand " + base64.StdEncoding.EncodeToString(utf16le(script))
}

func utf16le(value string) []byte {
	encoded := make([]byte, 0, len(value)*2)
	for _, r := range value {
		if r > 0xffff {
			r = '?'
		}
		encoded = append(encoded, byte(r), byte(r>>8))
	}
	return encoded
}

func envelope(inner string) string {
	return `<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope" xmlns:w="http://schemas.dmtf.org/wbem/wsman/1/wsman.xsd" xmlns:rsp="http://schemas.microsoft.com/wbem/wsman/1/windows/shell"><s:Header><w:To mustUnderstand="true">http://windows-host:5985/wsman</w:To><w:ReplyTo><w:Address mustUnderstand="true">http://schemas.xmlsoap.org/ws/2004/08/addressing/role/anonymous</w:Address></w:ReplyTo><w:MaxEnvelopeSize mustUnderstand="true">153600</w:MaxEnvelopeSize></s:Header><s:Body>` + inner + `</s:Body></s:Envelope>`
}

func selector(shellID string) string {
	return `<w:SelectorSet><w:Selector Name="ShellId">` + xmlEscape(shellID) + `</w:Selector></w:SelectorSet>`
}

func firstXMLValue(data []byte, local string) string {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	for {
		token, err := decoder.Token()
		if err != nil {
			return ""
		}
		start, ok := token.(xml.StartElement)
		if !ok || start.Name.Local != local {
			continue
		}
		var value string
		if err := decoder.DecodeElement(&value, &start); err != nil {
			return ""
		}
		return strings.TrimSpace(value)
	}
}

func xmlEscape(value string) string {
	var buf bytes.Buffer
	_ = xml.EscapeText(&buf, []byte(value))
	return buf.String()
}
