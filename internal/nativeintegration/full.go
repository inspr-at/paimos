// SPDX-License-Identifier: AGPL-3.0-only

package nativeintegration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"time"

	"github.com/inspr-at/paimos/internal/chat"
	"github.com/inspr-at/paimos/internal/nativecontract"
	"github.com/inspr-at/paimos/internal/nativesessions"
	"github.com/inspr-at/paimos/internal/nativesessions/consent"
	"github.com/inspr-at/paimos/internal/nativesessions/receipts"
	"github.com/inspr-at/paimos/internal/nativesessions/store"
	"github.com/inspr-at/paimos/internal/nativesessions/stream"
	"github.com/inspr-at/paimos/internal/nativestream"
	"github.com/jackc/pgx/v5/pgxpool"
)

// FullOptions installs only the person-invoked native surface. The server and
// owning daemon must each opt in; this neither approves consent nor starts one.
type FullOptions struct {
	Enabled bool
	Pool    *pgxpool.Pool
	Chat    *chat.Module
	Now     func() time.Time
}

type Full struct {
	HTTP      *nativesessions.Module
	Store     *store.Store
	Services  nativecontract.Services
	Admission *RootAuthority
}

// Capabilities describes this composition protocol, not a vendor login or an
// operator's qualification of a binary. Those remain local launch requirements.
func Capabilities() nativecontract.Capabilities {
	sum := sha256.Sum256([]byte("aeon-native-composition/1"))
	return nativecontract.Capabilities{Input: true, Steer: "queued", Interrupt: true, Stop: true, CorrelatedReceipts: true, Quota: "unsupported", QualificationDigest: hex.EncodeToString(sum[:]), EventsSupported: true, InterruptSupported: true, ChatSupported: true}
}

// NewFull composes the production consent, start, command, receipt, final and
// relay services. Off retains none of the supplied authorities or resources.
func NewFull(o FullOptions) (*Full, error) {
	if !o.Enabled {
		return &Full{HTTP: nativesessions.NewDisabled()}, nil
	}
	if o.Pool == nil || o.Chat == nil {
		return nil, refuse(nativecontract.ReasonQualificationMissing)
	}
	c := consent.Authority{Capabilities: Capabilities(), Now: o.Now}
	a := receipts.CurrentAuthority{Consent: c}
	repo, err := receipts.New(o.Pool, a, o.Chat, o.Now)
	if err != nil {
		return nil, err
	}
	s, err := store.New(o.Pool, store.Options{Authority: c, Observer: store.PrivateObserver{}, Now: o.Now})
	if err != nil {
		return nil, err
	}
	projector, err := nativestream.NewProjector(nativestream.ExportPolicy{Text: func(_ context.Context, _ nativecontract.Binding, text string) (string, error) { return text, nil }})
	if err != nil {
		return nil, err
	}
	relay, err := stream.NewRelay(projector, nil)
	if err != nil {
		return nil, err
	}
	s.BindProduction(c, repo, relay)
	services := nativecontract.Services{Store: s, Frames: relay, Finals: s, Qualified: true}
	module, err := nativesessions.New(services)
	if err != nil {
		return nil, err
	}
	return &Full{HTTP: module, Store: s, Services: services, Admission: &RootAuthority{Pool: o.Pool, Grants: c, Now: o.Now}}, nil
}
