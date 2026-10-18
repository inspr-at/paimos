// SPDX-License-Identifier: AGPL-3.0-only
package modelreport

import (
	"encoding/json"
	"errors"
	"strings"
	"time"
)

const CatalogModelsMax = 500
const CatalogBytesMax = 1 << 20

// CatalogField retains the exact public native field. Missing fields are absent.
type CatalogField struct {
	Value json.RawMessage `json:"value"`
	Path  string          `json:"field_path"`
}

type CatalogModel struct {
	Model    string                  `json:"model"`
	Provider string                  `json:"provider,omitempty"`
	Effort   string                  `json:"effort,omitempty"`
	Facts    map[string]CatalogField `json:"facts"`
}

type CatalogProbe struct {
	AccountID       string    `json:"account_id"`
	AccountKey      string    `json:"account_key"`
	Harness         string    `json:"harness"`
	BindingRevision int64     `json:"binding_revision"`
	RequestID       string    `json:"request_id"`
	RequestedAt     time.Time `json:"requested_at"`
}

type CatalogReport struct {
	RequestID       string         `json:"request_id"`
	BindingRevision int64          `json:"binding_revision"`
	DaemonID        string         `json:"daemon_id"`
	Generation      string         `json:"daemon_generation"`
	State           string         `json:"state"`
	Models          []CatalogModel `json:"models"`
}

// ValidateCatalog bounds and allowlists public metadata before any database work.
// It deliberately does not assign a numerical meaning to a native level.
func ValidateCatalog(state string, models []CatalogModel) error {
	if models == nil || len(models) > CatalogModelsMax {
		return errors.New("invalid catalog models")
	}
	switch state {
	case "fresh", "limited":
	case "unsupported", "stale", "timeout":
		if len(models) != 0 {
			return errors.New("failed catalog contains models")
		}
	default:
		return errors.New("invalid catalog state")
	}
	seen := map[string]bool{}
	for _, m := range models {
		if !ValidTuple(m.Model, "default") || seen[m.Model] || m.Effort != "" && !ValidTuple(m.Model, m.Effort) || len(m.Provider) > 64 || m.Provider != "" && !effortRE.MatchString(m.Provider) || len(m.Facts) > 16 {
			return errors.New("invalid catalog model")
		}
		seen[m.Model] = true
		for name, f := range m.Facts {
			if f.Path == "" || len(f.Path) > 128 || strings.ContainsAny(f.Path, "\x00\r\n") || len(f.Value) > 8192 || !json.Valid(f.Value) {
				return errors.New("invalid catalog field")
			}
			switch name {
			case "display_name", "model_version", "line", "default_level", "successor", "retirement_at", "lifecycle", "hidden", "tools", "thinking", "native_levels":
			default:
				return errors.New("unknown catalog fact")
			}
			if strings.TrimSpace(string(f.Value)) == "null" {
				continue
			}
			switch name {
			case "display_name", "model_version", "line", "default_level", "successor", "retirement_at", "lifecycle":
				var s string
				if json.Unmarshal(f.Value, &s) != nil || len(s) > 256 || strings.ContainsAny(s, "\x00\r\n") {
					return errors.New("invalid catalog text")
				}
				if name == "default_level" && !ValidTuple(m.Model, s) {
					return errors.New("invalid native default")
				}
			case "hidden", "tools", "thinking":
				var b bool
				if json.Unmarshal(f.Value, &b) != nil {
					return errors.New("invalid catalog boolean")
				}
			case "native_levels":
				var levels []string
				if json.Unmarshal(f.Value, &levels) != nil || levels == nil || len(levels) > 32 {
					return errors.New("invalid native levels")
				}
				known := map[string]bool{}
				for _, level := range levels {
					if !ValidTuple(m.Model, level) || known[level] {
						return errors.New("invalid native level")
					}
					known[level] = true
				}
			default:
				return errors.New("unknown catalog fact")
			}
		}
	}
	return nil
}
