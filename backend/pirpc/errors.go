// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package pirpc

import "errors"

var (
	ErrFrameTooLarge       = errors.New("pi rpc frame exceeds size limit")
	ErrMalformedFrame      = errors.New("pi rpc frame is malformed")
	ErrUnsolicitedResponse = errors.New("pi rpc response has no pending correlation")
	ErrDuplicateResponse   = errors.New("pi rpc duplicate response ignored")
	ErrLateResponse        = errors.New("pi rpc late response ignored")
	ErrStreamEnded         = errors.New("pi rpc event stream ended")
	ErrProcessExited       = errors.New("pi rpc process exited")
	ErrProcessUnavailable  = errors.New("pi rpc process is unavailable")
	ErrCommandRejected     = errors.New("pi rpc command rejected")
	ErrInvalidCorrelation  = errors.New("pi rpc correlation id is invalid")
)
