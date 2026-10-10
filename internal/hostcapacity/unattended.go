// SPDX-License-Identifier: AGPL-3.0-only
package hostcapacity

import (
	"errors"
	"time"
)

// UnattendedCapability negotiates optional telemetry with older servers.
const UnattendedCapability = "unattended-v1"

type UnattendedSignals struct {
	LoginSession string `json:"login_session"`
	IdleSleep    string `json:"idle_sleep"`
	FileVault    string `json:"filevault"`
}

func (s UnattendedSignals) Validate() error {
	if s.LoginSession != "unknown" && s.LoginSession != "ready" && s.LoginSession != "required" ||
		s.IdleSleep != "unknown" && s.IdleSleep != "enabled" && s.IdleSleep != "disabled" ||
		s.FileVault != "unknown" && s.FileVault != "on" && s.FileVault != "off" {
		return errors.New("invalid unattended host signals")
	}
	return nil
}

type UnattendedView struct {
	Status             string `json:"status"`
	Reason             string `json:"reason"`
	Message            string `json:"message"`
	AfterRebootReason  string `json:"after_reboot_reason"`
	AfterRebootMessage string `json:"after_reboot_message"`
}

// EvaluateUnattended describes current observations separately from reboot
// recovery. It neither grants execution nor changes ordinary capacity policy.
// A missing heartbeat cannot prove whether a Mac slept, rebooted or lost its
// network, and must never be treated as an available host by routine dispatch.
func EvaluateUnattended(platform, state string, s *UnattendedSignals, at *time.Time, now time.Time) UnattendedView {
	v := UnattendedView{Status: "wait", AfterRebootReason: "unattended_reboot_unknown", AfterRebootMessage: "Unattended recovery after reboot is unconfirmed."}
	if platform == "darwin" {
		v.AfterRebootReason = "unattended_login_after_reboot"
		v.AfterRebootMessage = "The Mac user service needs a user login after reboot."
		if s != nil && s.FileVault == "on" {
			v.AfterRebootReason = "unattended_filevault_after_reboot"
			v.AfterRebootMessage = "FileVault unlock and a user login are needed after reboot."
		}
	}
	switch {
	case state != "connected":
		v.Reason, v.Message = "unattended_computer_disconnected", "The computer is not connected for new work."
	case platform != "darwin":
		v.Reason, v.Message = "unattended_platform_unsupported", "Unattended readiness is not qualified for this platform."
	case at == nil:
		v.Reason, v.Message = "unattended_unreported", "No unattended host report has been received."
	case at.After(now) || now.Sub(*at) > time.Minute:
		v.Reason, v.Message = "unattended_host_unreachable", "No recent host report; the Mac may be asleep, restarting, waiting for login or unreachable."
	case s == nil || s.Validate() != nil:
		v.Reason, v.Message = "unattended_signals_unknown", "Unattended host signals are missing or unreadable; update agentd if needed."
	case s.LoginSession == "required":
		v.Reason, v.Message = "unattended_login_required", "The daemon user needs an active Mac login session."
	case s.LoginSession != "ready":
		v.Reason, v.Message = "unattended_login_unknown", "The daemon user's Mac login session could not be confirmed."
	case s.IdleSleep == "enabled":
		v.Reason, v.Message = "unattended_sleep_enabled", "System idle sleep is enabled for the current power profile."
	case s.IdleSleep != "disabled":
		v.Reason, v.Message = "unattended_sleep_unknown", "System idle sleep settings could not be read."
	default:
		v.Status = "ready"
		v.Message = "Recent report confirms a user login and disabled system idle sleep; manual sleep, lid closure and restart can still interrupt work."
	}
	return v
}
