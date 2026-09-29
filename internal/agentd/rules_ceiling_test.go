// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/rules"
)

func TestClaudeBridgeRulesByteCeiling(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal(err)
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	bridge, err := claudeAssets.ReadFile("claudeassets/bridge.mjs")
	if err != nil {
		t.Fatal(err)
	}
	bridgePath, sdkPath := filepath.Join(root, "bridge.mjs"), filepath.Join(root, "sdk.mjs")
	if err = os.WriteFile(bridgePath, bridge, 0600); err != nil {
		t.Fatal(err)
	}
	// No model process or network: the local SDK fixture checks exact append bytes.
	sdk := `export function query({options}) {
 if(Buffer.byteLength(options.systemPrompt.append)!==Number(process.argv[6]))throw Error('changed rules');
 return {close(){},streamInput:async()=>{},async *[Symbol.asyncIterator](){
 yield {type:'system',subtype:'init',session_id:'fixture',capabilities:[]};
 yield {type:'result',subtype:'success',is_error:false};}}
 }`
	if err = os.WriteFile(sdkPath, []byte(sdk), 0600); err != nil {
		t.Fatal(err)
	}
	for _, size := range []int{12000, 12001, rules.MaxBytes, rules.MaxBytes + 1} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			// UTF-8 and JSON escaping must not be confused with decoded byte size.
			body := strings.Repeat("界\"", size/4) + strings.Repeat("x", size%4)
			raw, _ := json.Marshal(map[string]any{"op": "start", "prompt": "fixture", "rules": body, "purpose": "pairing_verification", "capabilities": []string{}})
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, node, bridgePath, sdkPath, "/bin/true", root, "fixture", fmt.Sprint(size))
			cmd.Stdin = strings.NewReader(string(raw) + "\n")
			out, err := cmd.CombinedOutput()
			if (err == nil) != (size <= rules.MaxBytes) {
				t.Fatalf("bridge %d: %v %s", size, err, out)
			}
			if size <= rules.MaxBytes && !strings.Contains(string(out), `"kind":"turn_completed"`) {
				t.Fatalf("rules not consumed: %s", out)
			}
		})
	}
}
