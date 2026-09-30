// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"sync"
	"time"

	"github.com/inspr-at/paimos/internal/capacity"
)

// An idle usage capability is compiled, never operator-configurable. Both the
// exact executable and the quota-neutral get_usage exchange must be verified
// together before adding a production entry. No production entry is approved
// yet: live verification belongs to the coordinator, not this fixture build.
type claudeUsageCapability struct {
	binaryPath, binarySHA256, version string
	// capture is the verified exchange; it receives the minimal child env.
	capture func(context.Context, string, []string) (json.RawMessage, error)
	decode  func(json.RawMessage, time.Time) []capacity.Reading
}

type capacityBinaryStamp struct {
	path  string
	size  int64
	mtime int64
}

var capacityBinaryCache struct {
	sync.Mutex
	digest map[capacityBinaryStamp]string
	hashes int
}

func exactCapacityBinary(path, digest string) bool {
	if len(digest) != 64 {
		return false
	}
	if _, err := pinnedExecutable(path); err != nil {
		return false
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return false
	}
	stamp := capacityBinaryStamp{path, info.Size(), info.ModTime().UnixNano()}
	capacityBinaryCache.Lock()
	cached, ok := capacityBinaryCache.digest[stamp]
	capacityBinaryCache.Unlock()
	if ok {
		return cached == digest
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, (512<<20)+1))
	if err != nil || n > 512<<20 {
		return false
	}
	sum := hex.EncodeToString(h.Sum(nil))
	capacityBinaryCache.Lock()
	if capacityBinaryCache.digest == nil {
		capacityBinaryCache.digest = map[capacityBinaryStamp]string{}
	}
	capacityBinaryCache.digest[stamp] = sum
	capacityBinaryCache.hashes++
	capacityBinaryCache.Unlock()
	return sum == digest
}
func (a *ClaudeAdapter) CanCaptureCapacity(key string) bool {
	c := a.usage
	if c == nil || c.version == "" || c.capture == nil || c.decode == nil || c.binaryPath != a.ClaudePath || !exactCapacityBinary(a.ClaudePath, c.binarySHA256) {
		return false
	}
	_, err := localHome(a.Homes, key)
	return err == nil
}
func (a *ClaudeAdapter) CaptureCapacity(ctx context.Context, key string) []capacity.Reading {
	if ctx.Err() != nil || !a.CanCaptureCapacity(key) {
		return nil
	}
	home, err := localHome(a.Homes, key)
	if err != nil {
		return nil
	}
	op, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	raw, err := a.usage.capture(op, home, claudeEnvironment(home, a.NodePath, a.ClaudePath))
	if err != nil || len(raw) > 64<<10 {
		return nil
	}
	now := time.Now().UTC()
	rs := a.usage.decode(raw, now)
	if len(rs) > 32 {
		return nil
	}
	for i := range rs {
		rs[i].Source = "agentd"
		rs[i].ReadAt = now
		rs[i].RunID = ""
		rs[i].Phase = ""
		if rs[i].Validate(now) != nil {
			return nil
		}
	}
	return rs
}
