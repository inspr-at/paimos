// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public
// License along with this program. If not, see <https://www.gnu.org/licenses/>.

// PAI-710 (epic PAI-703). Speech-to-text provider settings for the voice
// intake workbench. Mirrors the ai_settings key discipline exactly: the
// key is admin-set, secretvault-encrypted at rest, never returned by the
// API (has_api_key only), and the browser never sees it — audio goes to
// the backend, which calls the provider.
//
// voice_base_url exists because ElevenLabs EU data residency
// (Enterprise) runs on an ISOLATED environment: a different hostname
// (api.eu.residency.elevenlabs.io) AND a different API key than the
// standard host. A key valid on one is invalid on the other — the two
// must never be collapsed into one constant (START research finding,
// PPM knowledge: START/provider-integration-findings).

package handlers

import (
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"

	"github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/secretvault"
)

const voiceSecretDomain = "ai:elevenlabs"

const (
	voiceDefaultBaseURL  = "https://api.elevenlabs.io"
	voiceDefaultSTTModel = "scribe_v1"
)

// VoiceSettings is the resolved shape. APIKey never leaves the process.
type VoiceSettings struct {
	Provider string `json:"provider"` // "" (off) | "elevenlabs"
	APIKey   string `json:"-"`
	HasKey   bool   `json:"has_api_key"`
	BaseURL  string `json:"base_url"`
	STTModel string `json:"stt_model"`
}

// LoadVoiceSettings reads the voice columns off the M74 singleton row.
func LoadVoiceSettings() (VoiceSettings, error) {
	var s VoiceSettings
	var encrypted []byte
	err := db.DB.QueryRow(
		`SELECT COALESCE(voice_provider,''), voice_api_key_encrypted,
		        COALESCE(voice_base_url,''), COALESCE(voice_stt_model,'')
		 FROM ai_settings WHERE id = 1`,
	).Scan(&s.Provider, &encrypted, &s.BaseURL, &s.STTModel)
	if errors.Is(err, sql.ErrNoRows) {
		return VoiceSettings{BaseURL: voiceDefaultBaseURL, STTModel: voiceDefaultSTTModel}, nil
	}
	if err != nil {
		return VoiceSettings{}, err
	}
	if len(encrypted) > 0 {
		plain, derr := secretvault.Decrypt(voiceSecretDomain, encrypted)
		if derr != nil {
			return VoiceSettings{}, derr
		}
		s.APIKey = string(plain)
		s.HasKey = true
	}
	if s.BaseURL == "" {
		s.BaseURL = voiceDefaultBaseURL
	}
	if s.STTModel == "" {
		s.STTModel = voiceDefaultSTTModel
	}
	return s, nil
}

// Available reports whether speech input can be offered.
func (s VoiceSettings) Available() bool {
	return s.Provider == "elevenlabs" && s.APIKey != ""
}

// GetVoiceSettings — admin read (RequireAdmin at the route). Never
// includes the key material.
func GetVoiceSettings(w http.ResponseWriter, r *http.Request) {
	s, err := LoadVoiceSettings()
	if err != nil {
		log.Printf("voice_settings load: %v", err)
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}
	jsonOK(w, s)
}

type voiceSettingsPayload struct {
	Provider string  `json:"provider"`
	APIKey   *string `json:"api_key"` // nil = keep, "" = clear
	BaseURL  string  `json:"base_url"`
	STTModel string  `json:"stt_model"`
}

// PutVoiceSettings — admin write, same key semantics as PutAISettings.
func PutVoiceSettings(w http.ResponseWriter, r *http.Request) {
	var p voiceSettingsPayload
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		jsonError(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	p.Provider = strings.TrimSpace(p.Provider)
	if p.Provider != "" && p.Provider != "elevenlabs" {
		jsonError(w, "unsupported voice provider", http.StatusBadRequest)
		return
	}
	if p.APIKey == nil {
		if _, err := db.DB.Exec(
			`UPDATE ai_settings
			 SET voice_provider = ?, voice_base_url = ?, voice_stt_model = ?,
			     updated_at = datetime('now')
			 WHERE id = 1`,
			p.Provider, strings.TrimSpace(p.BaseURL), strings.TrimSpace(p.STTModel),
		); err != nil {
			log.Printf("voice_settings update: %v", err)
			jsonError(w, "internal error", http.StatusInternalServerError)
			return
		}
	} else {
		var encrypted []byte
		if *p.APIKey != "" {
			ct, eerr := secretvault.Encrypt(voiceSecretDomain, []byte(*p.APIKey))
			if eerr != nil {
				log.Printf("voice_settings encrypt: %v", eerr)
				jsonError(w, "internal error", http.StatusInternalServerError)
				return
			}
			encrypted = ct
		}
		if _, err := db.DB.Exec(
			`UPDATE ai_settings
			 SET voice_provider = ?, voice_api_key_encrypted = ?,
			     voice_base_url = ?, voice_stt_model = ?,
			     updated_at = datetime('now')
			 WHERE id = 1`,
			p.Provider, encrypted, strings.TrimSpace(p.BaseURL), strings.TrimSpace(p.STTModel),
		); err != nil {
			log.Printf("voice_settings update: %v", err)
			jsonError(w, "internal error", http.StatusInternalServerError)
			return
		}
	}
	s, err := LoadVoiceSettings()
	if err != nil {
		log.Printf("voice_settings reload: %v", err)
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}
	jsonOK(w, s)
}
