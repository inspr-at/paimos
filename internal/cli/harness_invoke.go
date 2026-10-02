// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/inspr-at/paimos/internal/harnesslaunch"
	"github.com/inspr-at/paimos/internal/ownedprocess"
)

// invoke extends the existing harness CLI so registry commands can apply Gemini
// generation settings without editing vendor or repository configuration.
func (rt *runtime) harnessInvoke() *Command {
	var harness, model, effort string
	var review bool
	return &Command{Name: "invoke", Short: "Run a pinned Gemini or OpenCode profile", Use: "harness invoke --harness NAME --model MODEL --effort EFFORT [--review] -- PROMPT", minArgs: 1, maxArgs: 1,
		addFlags: func(fs *flagSet) {
			fs.string(&harness, "harness", 0, "gemini or opencode")
			fs.string(&model, "model", 0, "exact vendor model")
			fs.string(&effort, "effort", 0, "thinking token budget or OpenCode variant")
			fs.bool(&review, "review", 0, "use the vendor review mode")
		}, run: func(args []string) error {
			if harness != "gemini" && harness != "opencode" || model == "" || strings.HasPrefix(model, "-") || effort == "" {
				return usagef("select gemini or opencode and an exact model/effort")
			}
			path, err := exec.LookPath(harness)
			if err != nil {
				return errors.New("vendor CLI unavailable")
			}
			path, err = filepath.EvalSymlinks(path)
			if err != nil {
				return err
			}
			argv := []string{"run", "--model", model, "--format", "json"}
			environment := os.Environ()
			if harness == "gemini" {
				settings, err := harnesslaunch.GeminiSettings(model, effort)
				if err != nil {
					return err
				}
				environment = replaceInvocationEnv(environment, "GEMINI_CLI_SYSTEM_SETTINGS_PATH", settings)
				argv = []string{"--model", model, "--output-format", "json"}
				if review {
					argv = append(argv, "--approval-mode", "plan")
				}
				argv = append(argv, "-p", args[0])
			} else {
				if !strings.Contains(model, "/") {
					return usagef("OpenCode requires provider/model")
				}
				if effort != "default" {
					argv = append(argv, "--variant", effort)
				}
				if review {
					argv = append(argv, "--agent", "plan")
				}
				argv = append(argv, "--", args[0])
			}
			ctx, cancel := signalContext()
			defer cancel()
			cmd := exec.Command(path, argv...)
			cmd.Env = environment
			cmd.Stdin, cmd.Stdout, cmd.Stderr = rt.stdin, rt.stdout, rt.stderr
			if !ownedprocess.Configure(cmd) {
				return errors.New("owned process groups unavailable")
			}
			if err = cmd.Start(); err != nil {
				return err
			}
			life := ownedprocess.Track(cmd)
			verified := life.Verify() == nil
			done := make(chan error, 1)
			go func() {
				if verified {
					done <- life.WaitGroup()
				} else {
					done <- life.Wait()
				}
			}()
			select {
			case err = <-done:
			case <-ctx.Done():
				_ = life.Signal(true)
				err = <-done
			}
			var exit *exec.ExitError
			if errors.As(err, &exit) {
				return &exitError{code: exit.ExitCode()}
			}
			return err
		}}
}

func replaceInvocationEnv(base []string, key, value string) []string {
	out := make([]string, 0, len(base)+1)
	for _, e := range base {
		if !strings.HasPrefix(e, key+"=") {
			out = append(out, e)
		}
	}
	return append(out, key+"="+value)
}
