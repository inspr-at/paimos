// SPDX-License-Identifier: AGPL-3.0-only

// Package nativeintegration composes the qualified LEAD pilot boundaries.
// Construction supplies no consent and enables no runtime or automatic lead.
package nativeintegration

import (
	"github.com/inspr-at/paimos/internal/nativecontract"
	"github.com/inspr-at/paimos/internal/nativeintegration/delivery"
	"github.com/inspr-at/paimos/internal/nativeintegration/root"
	"github.com/inspr-at/paimos/internal/nativesessions"
)

// Options is supplied only by the server/owning-supervisor composition, never
// from an API body. Enablement still requires the coordinator's terms, native
// actor/history/tool grant and pin qualification. Supplying ports issues none.
type Options struct {
	Enabled   bool
	Services  nativecontract.Services
	Host      nativecontract.Host
	Launcher  nativecontract.Launcher
	Admission *RootAuthority
}

type Pilot struct {
	HTTP      *nativesessions.Module
	Host      nativecontract.Host
	Launcher  nativecontract.Launcher
	Admission *RootAuthority
}

// New defaults off even when qualified-looking ports are supplied. It mounts
// only C0's disabled routes until ALL production ports are deliberately wired;
// neither construction nor a person request can enable automatic LeadRuntime.
func New(opts Options) (*Pilot, error) {
	if !opts.Enabled {
		return &Pilot{HTTP: nativesessions.NewDisabled()}, nil
	}
	if opts.Host == nil || opts.Launcher == nil || opts.Admission == nil || opts.Admission.Pool == nil || opts.Admission.Grants == nil {
		return nil, refuse(nativecontract.ReasonQualificationMissing)
	}
	module, err := nativesessions.New(opts.Services)
	if err != nil {
		return nil, err
	}
	return &Pilot{HTTP: module, Host: opts.Host, Launcher: opts.Launcher, Admission: opts.Admission}, nil
}

// RootAuthority retains the original admission API without coupling the start
// store's authority tests to the production composition.
type RootAuthority = root.Authority

func refuse(reason nativecontract.Reason) error {
	return &nativecontract.ValidationError{Reason: reason, Path: "$"}
}

// Delivery is shared with the owning daemon without importing the server's
// database/chat composition into agentd.
type Delivery = delivery.Delivery
type DeliveryOptions = delivery.DeliveryOptions

var NewDelivery = delivery.New
