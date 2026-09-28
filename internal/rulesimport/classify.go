// SPDX-License-Identifier: AGPL-3.0-only

package rulesimport

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	roleFile    = regexp.MustCompile(`^AGENTS-AGENT-([A-Z0-9]+(?:-[A-Z0-9]+)*)\.md$`)
	domainFile  = regexp.MustCompile(`^AGENTS-DOMAIN-([A-Z0-9]+(?:-[A-Z0-9]+)*)\.md$`)
	profileFile = regexp.MustCompile(`^AGENTS-PROFILE(?:-[A-Z0-9]+(?:-[A-Z0-9]+)*)?\.md$`)
)

func classify(path, section string, size int) (SourceFile, error) {
	base := filepath.Base(path)
	file := SourceFile{Path: path, Base: base, Bytes: size}
	switch {
	case base == "AGENTS-KERNEL.md" || base == "AGENTS-KERNEL-PRIVATE.md":
		file.Kind = "kernel"
		file.Layer = LayerCompany
		file.Trust = ContextTemplate
		file.Placement = PlacementAlwaysOn
	case base == "AGENTS-CORE.md":
		file.Kind = "core"
		file.Layer = LayerCompany
		file.Trust = ContextTemplate
		file.Placement = PlacementOnDemand
	case domainFile.MatchString(base):
		file.Kind = "domain"
		file.Layer = LayerCompany
		file.Trust = ContextTemplate
		file.Placement = PlacementOnDemand
	case roleFile.MatchString(base):
		file.Kind = "role"
		file.Layer = LayerAgent
		file.Trust = ContextTemplate
		file.Role = strings.ToLower(roleFile.FindStringSubmatch(base)[1])
		file.Placement = PlacementAlwaysOn
		if size > AlwaysOnBudget {
			file.Placement = PlacementOnDemand
		}
	case profileFile.MatchString(base):
		file.Kind = "profile"
		file.Layer = LayerPerson
		file.Trust = ContextPerson
		file.Placement = PlacementOnDemand
		if section == SectionKernel {
			return SourceFile{}, fmt.Errorf("%w: %s has no kernel section", ErrMixedContext, base)
		}
	case base == "AGENTS.md":
		file.Kind = "repo"
		file.Layer = LayerProject
		file.Trust = ContextProject
		file.Placement = PlacementAlwaysOn
		if section == SectionPersonal {
			file.Layer = LayerPerson
			file.Trust = ContextPerson
		}
	case base == "CLAUDE.md":
		file.Kind = "claude"
		file.Layer = LayerCompany
		file.Trust = ContextTemplate
		file.Placement = PlacementAlwaysOn
		if section == SectionPersonal {
			file.Layer = LayerPerson
			file.Trust = ContextPerson
		}
	default:
		return SourceFile{}, fmt.Errorf("%w: %s", ErrUnrecognizedFile, base)
	}
	if trust, ok := privatePath(path); ok && file.Trust != ContextPerson {
		file.Trust = trust
	}
	return file, nil
}

func privatePath(path string) (TrustContext, bool) {
	base := filepath.Base(path)
	if base == "AGENTS-KERNEL-PRIVATE.md" || (strings.HasPrefix(base, "AGENTS-") && strings.Contains(strings.ToUpper(base), "PRIVATE")) {
		return ContextPrivate, true
	}
	for _, component := range strings.Split(filepath.ToSlash(path), "/") {
		if strings.EqualFold(component, "doctrine-private") {
			return ContextPrivate, true
		}
	}
	return "", false
}

func contextAllowed(plan, file TrustContext) bool {
	switch plan {
	case ContextTemplate:
		return file == ContextTemplate
	case ContextPrivate:
		return file == ContextPrivate || file == ContextProject
	case ContextProject:
		return file == ContextProject
	case ContextPerson:
		return file == ContextPerson
	default:
		return false
	}
}

var privateMarker = regexp.MustCompile(`(?i)(private[ _-]+kernel|doctrine-private|personal[ _-]+(?:profile|details|working agreements)|AGENTS-PROFILE|\*\*(?:user|workspace|email|identity)\*\*\s*:)`)
var personalMarker = regexp.MustCompile(`(?im)^#{1,6}\s+personal\b|[A-Z0-9._%+-]+@[A-Z0-9.-]+\.[A-Z]{2,}`)

// A filename never establishes that generic instructions are public. The
// explicit template annotation asserts intent, but cannot override private
// markers anywhere in the complete file, even when selecting a subsection.
func classifyContent(file *SourceFile, body, section string, plan TrustContext) error {
	private := file.Trust == ContextPrivate || privateMarker.MatchString(body)
	personal := file.Trust == ContextPerson || personalMarker.MatchString(body)
	if plan == ContextTemplate && (private || personal) {
		return fmt.Errorf("%w: private or personal markers prohibit public-template output; use an explicit local context", ErrMixedContext)
	}
	if private {
		file.Trust = ContextPrivate
		if section == SectionPersonal && plan == ContextPerson {
			file.Trust = ContextPerson
		}
	} else if personal && section == SectionAll {
		file.Trust = ContextPerson
		if plan == ContextPrivate {
			file.Trust = ContextPrivate
		}
	}
	if file.Kind == "repo" || file.Kind == "claude" {
		if plan == ContextTemplate {
			if !strings.Contains(body, "<!-- aeon-context: template -->") {
				return fmt.Errorf("%w: generic instructions need explicit local context or an aeon-context template annotation", ErrMixedContext)
			}
			file.Trust = ContextTemplate
		} else if !private && !(personal && section == SectionAll) {
			file.Trust = plan
		}
		// CLAUDE.md has no inherent company authority. Its explicit context sets
		// the local proposal layer, just as for a repository AGENTS.md.
		file.Layer = LayerProject
		if plan == ContextPerson {
			file.Layer = LayerPerson
		}
	}
	return nil
}
