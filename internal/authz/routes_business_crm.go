// SPDX-License-Identifier: AGPL-3.0-only

package authz

// Route and scope declarations for business/crm.
func init() {
	registerRoutes("business_crm", map[string]string{
		"DELETE /api/crm/contacts/{contactId}":                                      "crm.write",
		"DELETE /api/crm/organisations/{organisationId}":                            "crm.write",
		"GET /api/crm/contacts/{contactId}":                                         "crm.read",
		"GET /api/crm/organisations":                                                "crm.read",
		"GET /api/crm/organisations/{organisationId}":                               "crm.read",
		"GET /api/crm/organisations/{organisationId}/contacts":                      "crm.read",
		"GET /api/crm/organisations/{organisationId}/note-ai":                       "crm.manage",
		"GET /api/crm/organisations/{organisationId}/related":                       "crm.read",
		"GET /api/crm/organisations/{organisationId}/sync-status":                   "crm.read",
		"GET /api/crm/projects/{projectId}/cooperation":                             "crm.read",
		"GET /api/crm/providers":                                                    "crm.read",
		"GET /api/crm/providers/search":                                             "crm.read",
		"PATCH /api/crm/contacts/{contactId}":                                       "crm.write",
		"PATCH /api/crm/organisations/{organisationId}":                             "crm.write",
		"PATCH /api/crm/organisations/{organisationId}/visibility":                  "crm.write",
		"POST /api/crm/contacts/{contactId}/principals":                             "crm.manage",
		"POST /api/crm/organisations":                                               "crm.write",
		"POST /api/crm/organisations/{organisationId}/contacts":                     "crm.write",
		"POST /api/crm/organisations/{organisationId}/note-ai/generate":             "crm.manage",
		"POST /api/crm/organisations/{organisationId}/note-rewrite":                 "crm.manage",
		"POST /api/crm/organisations/{organisationId}/note-rewrite/{draftId}/apply": "crm.manage",
		"POST /api/crm/organisations/{organisationId}/number/reformat":              "crm.write",
		"POST /api/crm/organisations/{organisationId}/primary-contact":              "crm.write",
		"POST /api/crm/organisations/{organisationId}/sync":                         "crm.write",
		"POST /api/crm/providers/{providerId}/import":                               "crm.manage",
		"PUT /api/crm/documents/{attachmentId}/metadata":                            "crm.write",
		"PUT /api/crm/projects/{projectId}/cooperation":                             "crm.write",
		"PUT /api/crm/projects/{projectId}/customer":                                "crm.write",
		"PUT /api/crm/providers/{providerId}/config":                                "crm.manage",
	})
}
