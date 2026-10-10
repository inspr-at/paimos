// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/client"
)

// rawAPI is used only for byte-oriented resources. It keeps the configured
// instance and credential while preserving response bytes for curl/downloads.
func (rt *runtime) rawResponse(method, path string, body []byte, contentType string) (*http.Response, string, error) {
	inst, err := rt.resolve()
	if err != nil {
		return nil, "", err
	}
	if !strings.HasPrefix(path, "/api/") && path != "/api" {
		return nil, "", usagef("API path must start with /api")
	}
	u, err := url.ParseRequestURI(path)
	if err != nil || u.IsAbs() || u.Host != "" || strings.HasPrefix(path, "//") {
		return nil, "", usagef("invalid API path")
	}
	req, err := http.NewRequestWithContext(rt.context(), method, inst.URL+path, bytes.NewReader(body))
	if err != nil {
		return nil, "", rt.fail(err, inst.APIKey)
	}
	req.Header.Set("Accept", "*/*")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("Authorization", "Bearer "+inst.APIKey)
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return nil, "", rt.fail(err, inst.APIKey)
	}
	return resp, inst.APIKey, nil
}

const maxRawResponseBytes = 64 << 20

func readRawResponse(resp *http.Response, limit int64) ([]byte, error) {
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > limit {
		return nil, fmt.Errorf("response exceeds %d bytes", limit)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var apiErr struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(raw, &apiErr)
		if apiErr.Error == "" {
			apiErr.Error = strings.TrimSpace(string(raw))
		}
		if len(apiErr.Error) > 240 {
			apiErr.Error = apiErr.Error[:240]
		}
		return nil, &client.StatusError{Status: resp.StatusCode, Message: apiErr.Error}
	}
	return raw, nil
}

func (rt *runtime) rawAPI(method, path string, body []byte, contentType string) ([]byte, error) {
	resp, key, err := rt.rawResponse(method, path, body, contentType)
	if err != nil {
		return nil, err
	}
	raw, err := readRawResponse(resp, maxRawResponseBytes)
	if err != nil {
		return nil, rt.fail(err, key)
	}
	return raw, nil
}

func (rt *runtime) cmdCurl() *Command {
	var method, data, dataFile string
	return &Command{Name: "curl", Short: "Call an Aeon API path with configured auth", Use: "curl <api-path> [-X METHOD] [--data JSON|--data-file PATH]", minArgs: 1, maxArgs: 1,
		addFlags: func(fs *flagSet) {
			fs.string(&method, "method", 'X', "HTTP method")
			fs.string(&data, "data", 0, "inline request body")
			fs.string(&dataFile, "data-file", 0, "request body file, or - for stdin")
		},
		run: func(args []string) error {
			if data != "" && dataFile != "" {
				return usagef("--data and --data-file are mutually exclusive")
			}
			path := strings.TrimSpace(args[0])
			if strings.HasPrefix(path, "//") || strings.Contains(path, "://") || strings.Contains(path, "#") {
				return usagef("curl requires an instance-relative API path")
			}
			if !strings.HasPrefix(path, "/") {
				path = "/" + path
			}
			if path != "/api" && !strings.HasPrefix(path, "/api/") {
				path = "/api" + path
			}
			method = strings.ToUpper(strings.TrimSpace(method))
			if method == "" {
				method = http.MethodGet
			}
			if _, err := http.NewRequest(method, "http://example.invalid/", nil); err != nil {
				return usagef("invalid HTTP method")
			}
			var body []byte
			if dataFile != "" {
				var err error
				if dataFile == "-" {
					body, err = io.ReadAll(io.LimitReader(rt.stdin, 8<<20+1))
				} else {
					body, err = os.ReadFile(dataFile)
				}
				if err != nil {
					return rt.fail(fmt.Errorf("read data file: %w", err), "")
				}
				if len(body) > 8<<20 {
					return usagef("request body is too large")
				}
			} else {
				body = []byte(data)
			}
			contentType := ""
			if len(body) > 0 {
				contentType = "application/json"
			}
			raw, err := rt.rawAPI(method, path, body, contentType)
			if err != nil {
				return err
			}
			if _, err := rt.stdout.Write(raw); err != nil {
				return err
			}
			if len(raw) == 0 || raw[len(raw)-1] != '\n' {
				_, err = fmt.Fprintln(rt.stdout)
			}
			return err
		},
	}
}
