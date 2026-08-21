// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.

package handlers

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/inspr-at/paimos/backend/externalstage"
)

// MountInternalExternalStageContractRoutes and
// MountExternalStageContractRoutes freeze the v1 router surface before M148
// and the service handlers exist. Each operation is a literal chi route; there
// is intentionally no caller-selected {action}. The temporary handler keeps
// every frozen route concealed until its transactional implementation lands.
func MountInternalExternalStageContractRoutes(r chi.Router) {
	r.Post(externalStageMountPath(externalstage.InternalCreatePath, "/api/agent-mode"), externalStageContractUnavailable)
	r.Post(externalStageMountPath(externalstage.InternalMintPath, "/api/agent-mode"), externalStageContractUnavailable)
	r.Post(externalStageMountPath(externalstage.InternalRotatePath, "/api/agent-mode"), externalStageContractUnavailable)
	r.Post(externalStageMountPath(externalstage.InternalRevokePath, "/api/agent-mode"), externalStageContractUnavailable)
}

func MountExternalStageContractRoutes(r chi.Router) {
	r.Get(externalStageMountPath(externalstage.ExternalPullPath, "/api"), externalStageContractUnavailable)
	r.Post(externalStageMountPath(externalstage.ExternalAcceptPath, "/api"), externalStageContractUnavailable)
	r.Post(externalStageMountPath(externalstage.ExternalReportPath, "/api"), externalStageContractUnavailable)
}

func externalStageMountPath(path, prefix string) string {
	return strings.TrimPrefix(path, prefix)
}

func externalStageContractUnavailable(w http.ResponseWriter, r *http.Request) {
	writeControlNotFound(w, r)
}
