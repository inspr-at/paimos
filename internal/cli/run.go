// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/term"

	"github.com/inspr-at/paimos/internal/client"
	"github.com/inspr-at/paimos/internal/version"
)

// Run executes the CLI. args[0] is the program name. Exit status 0 is success,
// 1 is a runtime or API failure, 2 is usage, and 3 means the verb is not
// served yet (see unsupportedCompat). MCP tools that still lack a handler
// report the same with "arrives in R1".
func Run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	rt := &runtime{stdin: stdin, stdout: stdout, stderr: stderr, program: "aeon"}
	if len(args) > 0 {
		rt.program = programName(args[0])
	}
	err := rt.execute(args)
	if err == nil {
		return 0
	}
	var ee *exitError
	if errors.As(err, &ee) {
		rt.printErr(ee)
		if ee.code == 0 {
			return 1
		}
		return ee.code
	}
	fmt.Fprintln(rt.stderr, rt.program+": "+redact(err.Error(), ""))
	return 1
}

func programName(argv0 string) string {
	base := strings.TrimSuffix(filepath.Base(argv0), ".exe")
	if base == "paimos" {
		return "paimos"
	}
	return "aeon"
}

type runtime struct {
	program            string
	stdin              io.Reader
	stdout             io.Writer
	stderr             io.Writer
	configPath         string
	instance           string
	jsonOut            bool
	agentName          string
	sessionID          string
	help               bool
	version            bool
	kinds              *kindTable
	messagingDeliverer localDeliverer
	personClient       *client.Client
}

func (rt *runtime) execute(args []string) error {
	rest := []string{}
	if len(args) > 1 {
		rest = args[1:]
	}
	root := rt.root()
	cmd, cmdArgs, err := rt.walk(root, rest)
	if err != nil {
		return usagef("%s", err.Error())
	}
	pos, err := rt.parse(cmd, cmdArgs)
	if err != nil {
		return usagef("%s", err.Error())
	}
	if rt.help {
		rt.writeHelp(cmd)
		return nil
	}
	if rt.version {
		fmt.Fprintln(rt.stdout, version.Version)
		return nil
	}
	if cmd.run == nil {
		if cmd.Name == "" {
			rt.writeHelp(cmd)
			return &exitError{code: 2}
		}
		return usagef("%s requires a subcommand", cmd.Name)
	}
	if err := cmd.checkArgs(pos); err != nil {
		return err
	}
	return cmd.run(pos)
}

func (rt *runtime) printErr(ee *exitError) {
	if ee.msg == "" {
		return
	}
	if rt.jsonOut && (ee.code == 1 || ee.code == 3) {
		_ = json.NewEncoder(rt.stderr).Encode(map[string]string{"error": ee.msg})
		return
	}
	fmt.Fprintln(rt.stderr, rt.program+": "+ee.msg)
}

func (rt *runtime) printJSON(v any) error {
	enc := json.NewEncoder(rt.stdout)
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

func (rt *runtime) fail(err error, secret string) error {
	return &exitError{code: 1, msg: redact(err.Error(), secret)}
}

func redact(msg, secret string) string {
	if secret == "" {
		return msg
	}
	return strings.ReplaceAll(msg, secret, "[redacted]")
}

func (rt *runtime) root() *Command {
	subs := []*Command{
		rt.cmdStatus(),
		rt.cmdStatusline(),
		rt.cmdAuth(),
		rt.cmdKeys(),
		rt.cmdScopes(),
		rt.cmdWhoami(""),
		rt.cmdIssue(),
		rt.cmdQueue(),
		rt.cmdRecur(),
		rt.cmdOutcome(),
		rt.cmdProject(),
		rt.cmdRelation(),
		rt.cmdRulesImport(),
		rt.cmdRulesTLDR(),
		rt.cmdRulesCompare(),
		rt.cmdRules(),
		rt.cmdDoctrine(),
		rt.cmdTag(),
		rt.cmdAttach(),
		rt.cmdExternalStage(),
		rt.cmdApply(),
		rt.cmdSchema(),
		rt.cmdDoctor(),
		rt.cmdCurl(),
		rt.cmdKnowledge(),
		rt.cmdSearch("search"),
		rt.cmdModel(),
		rt.cmdCapacity(),
		rt.cmdAgents(),
		rt.cmdUse(),
		rt.cmdOnboard(),
		rt.cmdSession(),
		rt.cmdHook(),
		rt.cmdTell(),
		rt.cmdListen(),
		rt.cmdMessage(),
		rt.cmdMCP(),
		rt.cmdAsk(),
		{
			Name:  "version",
			Short: "Print the calendar version",
			Use:   "version",
			run: func(args []string) error {
				fmt.Fprintln(rt.stdout, version.Version)
				return nil
			},
		},
	}
	subs = append(subs, rt.compatStubs()...)
	defaultConfig := "~/.aeon/config.yaml"
	if rt.program == "paimos" {
		defaultConfig = "~/.paimos/config.yaml"
	}
	return &Command{
		Short: "Agents-first command line for PAIMOS AEON",
		Long:  "Named instances and the default live in " + defaultConfig + ". Agent API keys are stored next to that file and are never printed.",
		Use:   "<command> [flags]",
		subs:  subs,
	}
}

func (rt *runtime) cmdAuth() *Command {
	return &Command{
		Name:  "auth",
		Short: "Log in and show the caller",
		Use:   "auth <login|whoami>",
		subs: []*Command{
			rt.cmdLogin(),
			rt.cmdWhoami("auth whoami"),
		},
	}
}

func (rt *runtime) cmdLogin() *Command {
	var urlFlag, nameFlag, keyFile string
	return &Command{
		Name:  "login",
		Short: "Store an instance URL and an agent API key",
		Long:  "The API key is read from --key-file or from stdin. A terminal stdin is not echoed. The key is stored beside the config file and is never printed.",
		Use:   "auth login --url URL [--name NAME] [--key-file PATH]",
		addFlags: func(fs *flagSet) {
			fs.string(&urlFlag, "url", 0, "instance URL")
			fs.string(&nameFlag, "name", 0, "instance name (default \"default\")")
			fs.string(&keyFile, "key-file", 0, "file containing the agent API key, or - for stdin")
		},
		run: func(args []string) error {
			return rt.login(urlFlag, nameFlag, keyFile)
		},
	}
}

func (rt *runtime) login(rawURL, name, keyFile string) error {
	if strings.TrimSpace(name) == "" {
		name = "default"
	}
	if err := validInstanceName(name); err != nil {
		return err
	}
	u, err := normalizeURL(rawURL)
	if err != nil {
		return err
	}
	key, err := rt.readSecret(keyFile, "API key")
	if err != nil {
		return err
	}
	me, err := client.New(u, key).Me(context.Background())
	if err != nil {
		return rt.fail(fmt.Errorf("login failed: %w", err), key)
	}
	cfg, path, err := rt.loadConfig()
	if err != nil {
		return err
	}
	if cfg.Instances == nil {
		cfg.Instances = map[string]fileInstance{}
	}
	cfg.Instances[name] = fileInstance{URL: u}
	if cfg.DefaultInstance == "" {
		cfg.DefaultInstance = name
	}
	if err := writeKey(keyPath(path, name), key); err != nil {
		return rt.fail(err, key)
	}
	if err := saveConfig(path, cfg); err != nil {
		return rt.fail(err, key)
	}
	if rt.jsonOut {
		return rt.printJSON(map[string]any{
			"ok":        true,
			"instance":  name,
			"url":       u,
			"config":    path,
			"principal": me.Principal.Name,
			"kind":      me.Principal.Kind,
			"default":   cfg.DefaultInstance == name,
		})
	}
	fmt.Fprintf(rt.stdout, "logged in as %s (%s) at %s\n", me.Principal.Name, me.Principal.Kind, u)
	fmt.Fprintf(rt.stdout, "saved instance %q to %s\n", name, path)
	if cfg.DefaultInstance == name {
		fmt.Fprintf(rt.stdout, "default_instance = %q\n", name)
	}
	return nil
}

func (rt *runtime) readSecret(file, label string) (string, error) {
	file = strings.TrimSpace(file)
	if file != "" && file != "-" {
		key, err := readKeyFile(file)
		if err != nil {
			return "", fmt.Errorf("read %s file: %w", label, err)
		}
		return key, nil
	}
	return rt.readSecretStdin(label)
}

func (rt *runtime) readSecretStdin(label string) (string, error) {
	if f, ok := rt.stdin.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		fmt.Fprintf(rt.stderr, "%s (input hidden): ", label)
		raw, err := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(rt.stderr)
		if err != nil {
			return "", fmt.Errorf("read %s: %w", label, err)
		}
		key, err := oneLineSecret(string(raw))
		if err != nil {
			return "", usagef("%s is required", label)
		}
		return key, nil
	}
	raw, err := io.ReadAll(io.LimitReader(rt.stdin, 8<<10))
	if err != nil {
		return "", fmt.Errorf("read %s: %w", label, err)
	}
	if len(raw) == 8<<10 {
		return "", usagef("%s is too long", label)
	}
	key, err := oneLineSecret(string(raw))
	if err != nil {
		return "", usagef("%s is required", label)
	}
	return key, nil
}

func normalizeURL(raw string) (string, error) {
	u := strings.TrimSpace(raw)
	if u == "" {
		return "", usagef("--url is required")
	}
	if !strings.Contains(u, "://") {
		u = "https://" + u
	}
	parsed, err := url.Parse(u)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", usagef("invalid instance URL")
	}
	return strings.TrimRight(parsed.String(), "/"), nil
}

func (rt *runtime) cmdWhoami(use string) *Command {
	if use == "" {
		use = "whoami"
	}
	name := "whoami"
	return &Command{
		Name:  name,
		Short: "Show the acting principal (GET /api/me)",
		Use:   use,
		run: func(args []string) error {
			return rt.whoami(context.Background())
		},
	}
}

func (rt *runtime) whoami(ctx context.Context) error {
	inst, err := rt.resolve()
	if err != nil {
		return err
	}
	me, err := client.New(inst.URL, inst.APIKey).Me(ctx)
	if err != nil {
		return rt.fail(err, inst.APIKey)
	}
	if rt.jsonOut {
		return rt.printJSON(map[string]any{
			"instance":  inst.Name,
			"url":       inst.URL,
			"principal": me.Principal,
			"tenant":    me.Tenant,
			"identity":  me.Identity,
		})
	}
	fmt.Fprintf(rt.stdout, "instance: %s (%s)\n", inst.Name, inst.URL)
	fmt.Fprintf(rt.stdout, "principal: %s (%s)\n", me.Principal.Name, me.Principal.Kind)
	fmt.Fprintf(rt.stdout, "tenant: %s (%s)\n", me.Tenant.Name, me.Tenant.Slug)
	return nil
}
