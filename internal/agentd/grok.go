// SPDX-License-Identifier: AGPL-3.0-only

package agentd

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"runtime"

	"github.com/inspr-at/paimos/internal/agentverification"
	"github.com/inspr-at/paimos/internal/grokprobe"
)

const (
	grokModel         = "grok-4.7"
	grokEffort        = "xhigh"
	grokConfigSHA256  = "3b9f1cefb4672eed5856debd6173bb82009546ad9b10f28d8372749d1bb8e259"
	grokProfileSHA256 = "9cd1990054adc092e004e649da746e4bb2844ae6debae4a8aa5495df5ecfb231"
)

//go:embed grokassets/config.toml grokassets/conversation.txt
var grokAssets embed.FS

// GrokBinding is operator-local. AEON receives only its opaque account key.
// The native adapter executes an isolated conversation without workspace I/O.
type GrokBinding = grokprobe.Binding

type GrokAdapter struct {
	Bindings map[string]GrokBinding
	Homes    map[string]string
	billing  *grokBillingCapability
}

func NewGrokAdapter(bindings ...map[string]GrokBinding) *GrokAdapter {
	a := &GrokAdapter{Bindings: map[string]GrokBinding{}}
	if len(bindings) > 0 {
		a.Bindings = bindings[0]
	}
	return a
}
func (*GrokAdapter) Name() string { return Grok }

// VerificationSupported matches the server's native Grok qualification. The
// account-specific binary, subject and confinement checks remain in startNative.
func (*GrokAdapter) VerificationSupported() bool {
	return grokVerificationSupported(runtime.GOOS, runtime.GOARCH)
}

func grokVerificationSupported(goos, goarch string) bool {
	return agentverification.For(Grok, goos, goarch).Supported
}

func (a *GrokAdapter) Start(ctx context.Context, r StartRequest, observe func(AdapterEvent)) (Process, error) {
	if err := validExecutionMode(r.Run, a); err != nil {
		return nil, err
	}
	if r.Profile.Harness != Grok || r.Profile.Model != grokModel || r.Profile.Effort != grokEffort || r.AccountKey == "" {
		return nil, errors.New("native Grok profile or account unavailable")
	}
	b, ok := a.Bindings[r.AccountKey]
	if !ok {
		return nil, errors.New("native Grok account unavailable")
	}
	return a.startNative(ctx, r, b, observe)
}

func sha256Hex(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }
