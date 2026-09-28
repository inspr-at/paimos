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
		return file == ContextTemplate || file == ContextProject
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
