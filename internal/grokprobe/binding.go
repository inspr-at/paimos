// SPDX-License-Identifier: AGPL-3.0-only

package grokprobe

type Binding struct {
	Variant         string `json:"variant"`
	BinaryPath      string `json:"binary_path"`
	AuthPath        string `json:"auth_path"`
	ScratchRoot     string `json:"scratch_root"`
	PrincipalSHA256 string `json:"principal_sha256"`
}

type Identity struct {
	Binding Binding
	Label   string
}
