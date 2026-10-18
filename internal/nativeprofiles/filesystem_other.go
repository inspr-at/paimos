// SPDX-License-Identifier: AGPL-3.0-only
//go:build !darwin && !linux

package nativeprofiles

type localFilesystem struct{}

func (localFilesystem) Lstat(string) (Directory, error) { return Directory{}, ErrQualification }
