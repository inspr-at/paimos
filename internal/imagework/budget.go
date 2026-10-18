// SPDX-License-Identifier: AGPL-3.0-only

// Package imagework bounds decoded image work across tenants and upload paths.
package imagework

import (
	"context"
	"errors"

	"golang.org/x/sync/semaphore"
)

type Kind uint8

const (
	Avatar Kind = iota
	Attachment
)

var ErrDimensions = errors.New("invalid or oversized image")

// One process-wide budget is intentional: per-tenant or per-Store limits would
// let concurrent avatar and attachment requests exhaust the same server.
// This bounds estimated live image work, not the Go heap or compressed bodies.
const capacity int64 = 256 << 20

var memory = semaphore.NewWeighted(capacity)

func (k Kind) limits() (pixels int64, dimension int, bytesPerPixel int64) {
	switch k {
	case Avatar:
		// Includes a possible 16-bit decoded PNG, canonical PNG encoding/buffer
		// growth, and CatmullRom's intermediate rows. No full-size crop or
		// orientation buffers are permitted in this pipeline.
		return 4 << 20, 4096, 32
	case Attachment:
		// A 16-bit decoded PNG needs at most eight bytes per pixel; reserve
		// additional headroom for decoder work. Fixed overhead below covers
		// the bounded 1600px preview, PNG encoding and small-image work.
		return 16 << 20, 8192, 12
	default:
		return 0, 0, 0
	}
}

// Check validates header dimensions without multiplying untrusted integers.
func (k Kind) Check(width, height int) error {
	pixels, dimension, _ := k.limits()
	if width < 1 || height < 1 || width > dimension || height > dimension || int64(width) > pixels/int64(height) {
		return ErrDimensions
	}
	return nil
}

// Acquire must run before full decoding. Hold its reservation through resizing,
// encoding and storage; call the returned release exactly once on every path.
// Waiters observe request cancellation and allocate no decoded pixel buffers.
func (k Kind) Acquire(ctx context.Context, width, height int) (release func(), err error) {
	if err := k.Check(width, height); err != nil {
		return nil, err
	}
	_, _, bytesPerPixel := k.limits()
	weight := int64(width)*int64(height)*bytesPerPixel + (32 << 20)
	if err := memory.Acquire(ctx, weight); err != nil {
		return nil, err
	}
	return func() { memory.Release(weight) }, nil
}
