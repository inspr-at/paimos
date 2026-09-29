// SPDX-License-Identifier: AGPL-3.0-only
//go:build !darwin && !linux

package agentd

import "os"

func openManagedDirectory(string, string) (*os.File, error)        { return nil, errManagedFile }
func openManagedFile(string, string, bool, bool) (*os.File, error) { return nil, errManagedFile }
func checkManagedRegular(*os.File) error                           { return errManagedFile }
func openManagedChild(*os.File, string) (*os.File, bool, error)    { return nil, false, errManagedFile }
