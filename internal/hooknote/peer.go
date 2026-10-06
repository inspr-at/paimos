// SPDX-License-Identifier: AGPL-3.0-only

package hooknote

import (
	"net"
	"os"
	"runtime"
)

// kernelSelfUID is the only foreign-uid comparison Snapshot uses.
// Tests replace it. Production is os.Getuid.
var kernelSelfUID = os.Getuid

// Process is a kernel observation: pid, start time and the loaded executable's
// path plus device/inode. Start time defeats pid reuse. The inode defeats an
// exec onto a replaced image. PIDVersion is the macOS pid version. Observe
// fills it from PROC_PIDUNIQIDENTIFIERINFO, which matches audit_token_t.val[7].
// It is zero where the kernel does not provide one.
type Process struct {
	PID        int
	UID        int
	Parent     int
	Started    string
	Executable string
	Dev        uint64
	Ino        uint64
	CWD        string
	PIDVersion uint32
}

// SameIdentity is true when both observations are the same process image.
// A zero pid version means that observation had no audit token; when both
// sides have one, they must match (exec and pid reuse change it on macOS).
func SameIdentity(a, b Process) bool {
	if a.PID < 1 || a.PID != b.PID || a.UID != b.UID || a.Parent != b.Parent || a.Started == "" || a.Started != b.Started {
		return false
	}
	if a.Executable == "" || a.Executable != b.Executable || a.CWD == "" || a.CWD != b.CWD {
		return false
	}
	if a.Dev != b.Dev || a.Ino != b.Ino || (a.Dev == 0 && a.Ino == 0) {
		return false
	}
	if a.PIDVersion != 0 && b.PIDVersion != 0 && a.PIDVersion != b.PIDVersion {
		return false
	}
	return true
}

// DaemonPin is public pairing metadata: the approved daemon's kernel identity.
// A socket path alone is not a pin.
type DaemonPin struct {
	PID        int    `json:"pid"`
	UID        int    `json:"uid"`
	Started    string `json:"started"`
	Executable string `json:"exe"`
	Dev        uint64 `json:"dev"`
	Ino        uint64 `json:"ino"`
	PIDVersion uint32 `json:"pidv"`
}

func (p DaemonPin) Valid() bool {
	if p.PID < 1 || p.UID < 0 || p.Started == "" || len(p.Started) > 128 || p.Executable == "" || p.Executable[0] != '/' {
		return false
	}
	if p.Dev == 0 && p.Ino == 0 {
		return false
	}
	if runtime.GOOS == "darwin" && p.PIDVersion == 0 {
		return false
	}
	return true
}

// MatchesPin reports whether the connected daemon is the approved process.
func MatchesPin(proc Process, pin DaemonPin) bool {
	if !pin.Valid() || proc.PID != pin.PID || proc.UID != pin.UID || proc.Started != pin.Started {
		return false
	}
	if proc.Executable != pin.Executable || proc.Dev != pin.Dev || proc.Ino != pin.Ino {
		return false
	}
	if pin.PIDVersion != 0 && proc.PIDVersion != pin.PIDVersion {
		return false
	}
	return true
}

// PinFrom copies the fields a client must pin before it writes a request.
func PinFrom(proc Process) DaemonPin {
	return DaemonPin{PID: proc.PID, UID: proc.UID, Started: proc.Started, Executable: proc.Executable, Dev: proc.Dev, Ino: proc.Ino, PIDVersion: proc.PIDVersion}
}

// Recheck reads the socket peer again. A pid-reuse or exec-in-place change
// since the accept snapshot fails closed.
func Recheck(c net.Conn, before Process) error {
	now, err := Snapshot(c)
	if err != nil || now.UID != os.Getuid() || !SameIdentity(before, now) {
		return ErrPeer
	}
	return nil
}
