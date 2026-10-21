// SPDX-License-Identifier: AGPL-3.0-only

package nativeattach

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"

	"github.com/inspr-at/paimos/internal/client"
)

// LocalPersonHeader selects the CLI projection. It grants no authority: the
// server still requires a person session and current target permissions.
const LocalPersonHeader = "X-Aeon-Native-Local-Person"
const LocalPersonVersion = "1"

// PersonProof carries an existing login session independently of the local
// socket bearer. It is invocation-scoped and never persisted by these clients.
type PersonProof struct{ token string }

func NewPersonProof(c *client.Client) (*PersonProof, error) {
	if c == nil || c.Token != "" || len(c.SessionToken) != 64 {
		return nil, errors.New("native attach requires an existing person session; API keys are refused")
	}
	raw, err := hex.DecodeString(c.SessionToken)
	defer clear(raw)
	if err != nil || len(raw) != 32 {
		return nil, errors.New("native attach requires an existing person session; API keys are refused")
	}
	return &PersonProof{token: c.SessionToken}, nil
}
func (PersonProof) Format(f fmt.State, _ rune) { _, _ = fmt.Fprint(f, "[native person proof]") }
func (PersonProof) MarshalJSON() ([]byte, error) {
	return nil, errors.New("person proof is local authority")
}
func (*PersonProof) UnmarshalJSON([]byte) error { return errors.New("person proof cannot be decoded") }

// Authorize preserves the independent socket Authorization header. The daemon
// verifies this cookie against its own pinned instance, never a caller URL.
func (p *PersonProof) Authorize(r *http.Request) {
	if p != nil {
		r.AddCookie(&http.Cookie{Name: "aeon_session", Value: p.token})
	}
}

type projectionKey struct{}
type projectionOptions struct{ control string }

func (projectionOptions) Format(f fmt.State, _ rune) {
	_, _ = fmt.Fprint(f, "[native local projection]")
}

// WithLocalPersonProjection is used only after public transport authentication.
// The value is a presentation selector and cannot substitute for authorization.
func WithLocalPersonProjection(ctx context.Context, control string) context.Context {
	return context.WithValue(ctx, projectionKey{}, projectionOptions{control: control})
}
func LocalPersonProjection(ctx context.Context) bool {
	_, local := ctx.Value(projectionKey{}).(projectionOptions)
	return local
}
func LocalControlCapability(ctx context.Context) string {
	options, _ := ctx.Value(projectionKey{}).(projectionOptions)
	return options.control
}
