// SPDX-License-Identifier: AGPL-3.0-only

package sessionusage

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const submitTimeout = 30 * time.Second

// Run parses usage records from in and writes the normalized document to out.
// When --submit is set, that absolute command receives each report on stdin.
func Run(args []string, in io.Reader, out, errOut io.Writer) error {
	opt, submit, submitArgs, priorPath, err := parseArgs(args)
	if err != nil {
		return err
	}
	if priorPath != "" {
		priors, err := readPriors(priorPath)
		if err != nil {
			return err
		}
		opt.Priors = priors
	}
	raw, err := io.ReadAll(io.LimitReader(in, maxInput+1))
	if err != nil {
		return err
	}
	if len(raw) > maxInput {
		return fmt.Errorf("%w: input exceeds %d bytes", ErrMalformed, maxInput)
	}
	result, err := Parse(bytes.NewReader(raw), opt)
	if err != nil {
		return err
	}
	if submit != "" {
		if err := submitReports(submit, submitArgs, result.Reports, errOut); err != nil {
			return err
		}
	}
	enc := json.NewEncoder(out)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	return enc.Encode(result)
}

func parseArgs(args []string) (Options, string, []string, string, error) {
	var opt Options
	var submit string
	var submitArgs []string
	var prior string
	for i := 0; i < len(args); i++ {
		take := func(name string) (string, error) {
			if i+1 >= len(args) || args[i+1] == "" {
				return "", &UsageError{Msg: name + " requires a value"}
			}
			i++
			if len(args[i]) > 1024 || strings.ContainsAny(args[i], "\r\n") {
				return "", &UsageError{Msg: name + " is invalid"}
			}
			return args[i], nil
		}
		switch args[i] {
		case "--help", "-h":
			return Options{}, "", nil, "", &UsageError{Msg: "session-usage-parse --source codex|cursor [--model ID] [--billing-mode unknown|api|subscription] [--subscription-label TEXT] [--account-id UUID] [--account-label TEXT] [--prior-file PATH] [--submit ABS] [--submit-arg ARG]"}
		case "--source":
			v, err := take("--source")
			if err != nil {
				return Options{}, "", nil, "", err
			}
			opt.Source = v
		case "--model":
			v, err := take("--model")
			if err != nil {
				return Options{}, "", nil, "", err
			}
			opt.Model = v
		case "--billing-mode":
			v, err := take("--billing-mode")
			if err != nil {
				return Options{}, "", nil, "", err
			}
			opt.BillingMode = v
		case "--subscription-label":
			v, err := take("--subscription-label")
			if err != nil {
				return Options{}, "", nil, "", err
			}
			opt.SubscriptionLabel = v
		case "--account-id":
			v, err := take("--account-id")
			if err != nil {
				return Options{}, "", nil, "", err
			}
			opt.AccountID = v
		case "--account-label":
			v, err := take("--account-label")
			if err != nil {
				return Options{}, "", nil, "", err
			}
			opt.AccountLabel = v
		case "--prior-file":
			v, err := take("--prior-file")
			if err != nil {
				return Options{}, "", nil, "", err
			}
			prior = v
		case "--submit":
			v, err := take("--submit")
			if err != nil {
				return Options{}, "", nil, "", err
			}
			submit = v
		case "--submit-arg":
			v, err := take("--submit-arg")
			if err != nil {
				return Options{}, "", nil, "", err
			}
			if len(submitArgs) == 8 {
				return Options{}, "", nil, "", &UsageError{Msg: "too many --submit-arg values"}
			}
			submitArgs = append(submitArgs, v)
		default:
			return Options{}, "", nil, "", &UsageError{Msg: "unknown argument " + args[i]}
		}
	}
	if opt.Source != "codex" && opt.Source != "cursor" {
		return Options{}, "", nil, "", &UsageError{Msg: "--source must be codex or cursor"}
	}
	if submit != "" {
		if !filepath.IsAbs(submit) {
			return Options{}, "", nil, "", &UsageError{Msg: "--submit must be an absolute path"}
		}
		info, err := os.Stat(submit)
		if err != nil || !info.Mode().IsRegular() {
			return Options{}, "", nil, "", &UsageError{Msg: "--submit must be a regular file"}
		}
	}
	return opt, submit, submitArgs, prior, nil
}

func readPriors(path string) ([]Prior, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 64*1024 {
		return nil, &UsageError{Msg: "--prior-file must be a regular file of at most 64KiB"}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ParsePriors(raw)
}

func submitReports(path string, args []string, reports []UsageReport, errOut io.Writer) error {
	for _, report := range reports {
		body, err := json.Marshal(report)
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(context.Background(), submitTimeout)
		cmd := exec.CommandContext(ctx, path, args...)
		cmd.Stdin = bytes.NewReader(body)
		cmd.Stdout = errOut
		cmd.Stderr = errOut
		err = cmd.Run()
		cancel()
		if err != nil {
			return fmt.Errorf("submit usage report: %w", err)
		}
	}
	return nil
}
