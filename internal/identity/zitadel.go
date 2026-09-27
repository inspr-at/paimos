// SPDX-License-Identifier: AGPL-3.0-only

package identity

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type zitadel struct {
	origin, orgID, token, tenantID string
	client                         *http.Client
}

// newZitadel supports local httptest endpoints without a live Zitadel call.
// Server setup uses FromEnv so the credential always comes from a file.
func newZitadel(origin, orgID, token, tenantID string) Provisioner {
	return &zitadel{origin: strings.TrimRight(origin, "/"), orgID: orgID, token: token, tenantID: tenantID,
		client: &http.Client{Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func (*zitadel) Name() string       { return "Zitadel" }
func (z *zitadel) TenantID() string { return z.tenantID }

func (z *zitadel) EnsureUser(ctx context.Context, email, displayName string) (Result, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	subject, err := z.find(ctx, email)
	if err != nil {
		return Result{}, &ProvisionError{}
	}
	if subject != "" {
		return Result{Status: "exists", Subject: subject}, nil
	}
	subject, err = z.create(ctx, email, displayName)
	if err != nil {
		return Result{}, &ProvisionError{}
	}
	if err := z.SendInvite(ctx, subject); err != nil {
		return Result{}, &ProvisionError{Subject: subject}
	}
	return Result{Status: "invited", Subject: subject}, nil
}

func (z *zitadel) find(ctx context.Context, email string) (string, error) {
	body := map[string]any{"query": map[string]any{"limit": 2}, "queries": []any{
		map[string]any{"organizationIdQuery": map[string]string{"organizationId": z.orgID}},
		map[string]any{"emailQuery": map[string]any{"emailAddress": email, "method": 1}},
	}}
	var out struct {
		Result []struct {
			UserID  string `json:"userId"`
			Details struct {
				ResourceOwner string `json:"resourceOwner"`
			} `json:"details"`
			Human struct {
				Email struct {
					Email string `json:"email"`
				} `json:"email"`
			} `json:"human"`
		} `json:"result"`
	}
	if err := z.post(ctx, "/v2/users", body, &out); err != nil {
		return "", err
	}
	if len(out.Result) > 1 {
		return "", errors.New("ambiguous identity")
	}
	if len(out.Result) == 0 {
		return "", nil
	}
	user := out.Result[0]
	if user.UserID == "" || user.Details.ResourceOwner != z.orgID || !strings.EqualFold(user.Human.Email.Email, email) {
		return "", errors.New("identity mismatch")
	}
	return user.UserID, nil
}

func (z *zitadel) create(ctx context.Context, email, displayName string) (string, error) {
	words := strings.Fields(displayName)
	if len(words) == 0 {
		return "", errors.New("display name required")
	}
	given, family := words[0], words[0]
	if len(words) > 1 {
		family = strings.Join(words[1:], " ")
	}
	body := map[string]any{"organizationId": z.orgID, "human": map[string]any{
		"profile": map[string]string{"givenName": given, "familyName": family, "displayName": displayName},
		"email":   map[string]string{"email": email},
	}}
	var out struct {
		ID string `json:"id"`
	}
	if err := z.post(ctx, "/v2/users/new", body, &out); err != nil {
		return "", err
	}
	if out.ID == "" {
		return "", errors.New("missing user ID")
	}
	return out.ID, nil
}

func (z *zitadel) SendInvite(ctx context.Context, subject string) error {
	if subject == "" {
		return errors.New("missing user ID")
	}
	return z.post(ctx, "/v2/users/"+url.PathEscape(subject)+"/invite_code", map[string]any{"sendCode": map[string]any{}}, nil)
}

func (z *zitadel) post(ctx context.Context, path string, body any, out any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return errors.New("invalid identity request")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, z.origin+path, bytes.NewReader(payload))
	if err != nil {
		return errors.New("invalid identity request")
	}
	req.Header.Set("Authorization", "Bearer "+z.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := z.client.Do(req)
	if err != nil {
		return errors.New("identity provider unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return errors.New("identity provider rejected request")
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out); err != nil {
		return errors.New("invalid identity response")
	}
	return nil
}
