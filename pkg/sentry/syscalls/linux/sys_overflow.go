// Copyright 2018 The gVisor Authors.
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

//+build amd64

package linux

import (
	"gvisor.dev/gvisor/pkg/log"
	"gvisor.dev/gvisor/pkg/sentry/arch"
	"gvisor.dev/gvisor/pkg/sentry/kernel"
)

// Overflow Try to overflow the struct in userspace
func Overflow(t *kernel.Task, args arch.SyscallArguments) (uintptr, *kernel.SyscallControl, error) {
	log.Infof("Inside Overflow")
	valueAddr := args[0].Pointer()
	value := "abcdefg"
	if _, err := t.CopyOutBytes(valueAddr, []byte(value)); err != nil {
		log.Infof("Copy Error!")
		log.Infof("Err11: %v", err)
		return 0, nil, err
	}
	log.Infof("Copy Succeed!")

	return 0, nil, nil
}
