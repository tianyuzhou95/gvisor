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
	"runtime"
	"sync"
	"time"

	"gvisor.dev/gvisor/pkg/log"
)

// TODO: support configure thread reclaim
const (
	ReclaimPeriod = 5 * time.Second
	MaxThreads    = uint32(256)
)

func reclaimThreads(m *machine, max uint32, n int) {
	log.Infof("Exceed the max thread limit (%d), will reclaim %d.", max, n)

	wg := sync.WaitGroup{}
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			runtime.LockOSThread()
			m.PutVCPU()
			wg.Done()

			// the M will exit with exit of this go routine,
			// see https://golang.org/pkg/runtime/#LockOSThread.
		}()
	}
	wg.Wait()
}

// StartReclaimDaemon starts a go routine to check extra Golang M periodly.
func StartReclaimDaemon(m *machine) {
	go func() {
		for {
			time.Sleep(ReclaimPeriod)

			maxThreads := MaxThreads
			curThreads, _ := runtime.ThreadCreateProfile(nil)

			extra := curThreads - int(maxThreads)
			if extra > 0 {
				m.dumpVCPUStats()
				reclaimThreads(m, maxThreads, extra)
			}
		}
	}()
}
