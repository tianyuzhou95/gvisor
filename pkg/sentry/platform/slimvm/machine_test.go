// Copyright 2026 The gVisor Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package slimvm

import (
	"testing"

	"gvisor.dev/gvisor/pkg/bitmap"
)

// newPoolMachine builds a machine with only the vCPU reuse pool initialized.
// It deliberately avoids newMachine so the test does not require the
// /dev/slimvm kernel module: getVCPUFromPool/cacheVCPU only touch the pool,
// the bitmap and vCPUsPoolSpin.
func newPoolMachine(poolSize int) *machine {
	return &machine{
		vCPUsPool:       make([]*vCPU, poolSize),
		vCPUsPoolBitmap: bitmap.New(uint32(poolSize)),
	}
}

// TestVCPUPoolEmpty verifies that draining an empty pool returns nil.
func TestVCPUPoolEmpty(t *testing.T) {
	m := newPoolMachine(100)
	if c := m.getVCPUFromPool(); c != nil {
		t.Fatalf("getVCPUFromPool on empty pool: got %v, want nil", c)
	}
}

// TestVCPUPoolRoundTrip caches a full pool of distinct vCPUs and then drains
// it, asserting that every cached vCPU comes back exactly once with no loss
// or duplication.
//
// The pool size is deliberately >64 so the underlying bitmap spans multiple
// 64-bit words, exercising slot indexing across word boundaries.
func TestVCPUPoolRoundTrip(t *testing.T) {
	const poolSize = 100
	m := newPoolMachine(poolSize)

	// Fill the pool with distinct sentinel vCPUs.
	want := make(map[*vCPU]bool)
	cached := make([]*vCPU, poolSize)
	for i := 0; i < poolSize; i++ {
		c := &vCPU{id: i}
		cached[i] = c
		want[c] = true
		m.cacheVCPU(c, uint64(i))
	}

	// Drain the pool; collect everything we get back.
	got := make(map[*vCPU]bool)
	for i := 0; i < poolSize; i++ {
		c := m.getVCPUFromPool()
		if c == nil {
			t.Fatalf("getVCPUFromPool returned nil at iteration %d, expected a cached vCPU", i)
		}
		if got[c] {
			t.Fatalf("getVCPUFromPool returned duplicate vCPU id=%d", c.id)
		}
		got[c] = true
	}

	// Pool must now be empty.
	if c := m.getVCPUFromPool(); c != nil {
		t.Fatalf("pool not empty after draining: got vCPU id=%d", c.id)
	}

	// The set drained must equal the set cached.
	if len(got) != len(want) {
		t.Fatalf("drained %d vCPUs, cached %d", len(got), len(want))
	}
	for c := range want {
		if !got[c] {
			t.Fatalf("cached vCPU id=%d was never returned", c.id)
		}
	}
}

// TestVCPUPoolReuseSlots verifies that a slot freed by getVCPUFromPool is
// reused by a subsequent cacheVCPU, i.e. the occupied/free bookkeeping in the
// bitmap stays consistent across interleaved operations.
func TestVCPUPoolReuseSlots(t *testing.T) {
	const poolSize = 70
	m := newPoolMachine(poolSize)

	// Fill the pool.
	for i := 0; i < poolSize; i++ {
		m.cacheVCPU(&vCPU{id: i}, uint64(i))
	}

	// Take one out, then put a fresh one back: should fit (slot reused).
	first := m.getVCPUFromPool()
	if first == nil {
		t.Fatal("getVCPUFromPool returned nil on a full pool")
	}
	fresh := &vCPU{id: 999}
	m.cacheVCPU(fresh, 999)

	// Drain everything and confirm the fresh vCPU is present and the count
	// is back to poolSize.
	seenFresh := false
	n := 0
	for {
		c := m.getVCPUFromPool()
		if c == nil {
			break
		}
		if c == fresh {
			seenFresh = true
		}
		n++
	}
	if n != poolSize {
		t.Fatalf("drained %d vCPUs, want %d", n, poolSize)
	}
	if !seenFresh {
		t.Fatal("freshly cached vCPU was not returned; slot was not reused")
	}
}
