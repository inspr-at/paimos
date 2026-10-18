// SPDX-License-Identifier: AGPL-3.0-only

// Package doctrinerepo defines the deployment-owned proposal boundary.
package doctrinerepo

import (
	"fmt"
	"regexp"
	"strings"
)

const DefaultPublic = "inspr-at/inspr-modules"
const DefaultPrivate = "inspr-at/inspr-doctrine-private"

var repositoryPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,38}/[A-Za-z0-9._-]{1,100}$`)

// Pair is validated host policy, never tenant-editable. An empty public name
// disables public proposals; the private boundary is always required.
type Pair struct {
	public  string
	private string
}

func Default() Pair { return Pair{public: DefaultPublic, private: DefaultPrivate} }

func Parse(public, private string) (Pair, error) {
	valid := func(name string) bool {
		if !repositoryPattern.MatchString(name) {
			return false
		}
		repo := strings.SplitN(name, "/", 2)[1]
		return repo != "." && repo != ".."
	}
	if public != "" && !valid(public) {
		return Pair{}, fmt.Errorf("AEON_DOCTRINE_PUBLIC_REPOSITORY must be owner/repository or explicitly empty to disable public proposals")
	}
	if !valid(private) {
		return Pair{}, fmt.Errorf("AEON_DOCTRINE_PRIVATE_REPOSITORY must be owner/repository; a private quotation boundary is required")
	}
	if strings.EqualFold(public, private) {
		return Pair{}, fmt.Errorf("AEON_DOCTRINE_PUBLIC_REPOSITORY and AEON_DOCTRINE_PRIVATE_REPOSITORY must be different")
	}
	return Pair{public: public, private: private}, nil
}

func (p Pair) Public() string  { return p.public }
func (p Pair) Private() string { return p.private }
func (p Pair) IsPublic(repository string) bool {
	return p.public != "" && repository == p.public
}
func (p Pair) IsPrivate(repository string) bool {
	return p.private != "" && repository == p.private
}
func (p Pair) Writable(repository, visibility string) bool {
	return p.private != "" && (p.IsPublic(repository) && visibility == "public" || p.IsPrivate(repository) && visibility == "private")
}
