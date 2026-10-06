// SPDX-License-Identifier: AGPL-3.0-only

package plugins

// Builtin returns a sealed registry of supplied first-party plugins.
// The retired Flow's PHAROS/JANUS stage plugins are no longer registered.
func Builtin(extra ...func() (Plugin, error)) (*Registry, error) {
	return BuiltinWithRegistration(nil, extra...)
}

// BuiltinWithRegistration runs after the first-party manifests are registered
// and before sealing. It lets a dependent job provider bind the same registry.
func BuiltinWithRegistration(register func(*Registry) error, extra ...func() (Plugin, error)) (*Registry, error) {
	reg := NewRegistry()
	for _, build := range extra {
		plug, err := build()
		if err != nil {
			return nil, err
		}
		if err := reg.Register(plug); err != nil {
			return nil, err
		}
	}
	if register != nil {
		if err := register(reg); err != nil {
			return nil, err
		}
	}
	reg.Seal()
	return reg, nil
}
