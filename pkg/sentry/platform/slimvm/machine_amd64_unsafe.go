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

//go:build amd64
// +build amd64

package slimvm

import (
	"syscall"
	"unsafe"

	"gvisor.dev/gvisor/pkg/abi/linux"
)

// loadSegments copies the current segments.
//
// This may be called from within the signal context and throws on error.
//
//go:nosplit
func (c *vCPU) loadSegments(tid uint64) {
	if _, _, errno := syscall.RawSyscall(
		syscall.SYS_ARCH_PRCTL,
		linux.ARCH_GET_FS,
		uintptr(unsafe.Pointer(&c.CPU.Registers().Fs_base)),
		0); errno != 0 {
		throw("getting FS segment")
	}
	if _, _, errno := syscall.RawSyscall(
		syscall.SYS_ARCH_PRCTL,
		linux.ARCH_GET_GS,
		uintptr(unsafe.Pointer(&c.CPU.Registers().Gs_base)),
		0); errno != 0 {
		throw("getting GS segment")
	}
	c.tid.Store(tid)
}

// setUserRegisters sets user registers in the vCPU.
func (c *vCPU) setUserRegisters(uregs *userRegs) error {
	c.vmxConfig.userRegs = *uregs
	return nil
}

// setSystemRegisters sets system registers.
func (c *vCPU) setSystemRegisters(sregs *systemRegs) error {
	c.vmxConfig.sysRegs = *sregs
	return nil
}

// setCPUID sets the CPUID to be used by the guest.
func (c *vCPU) setCPUID() error {
	return nil
}

// setSystemTime sets the TSC for the vCPU.
//
// In SlimVM, the guest shares the host TSC directly, so no TSC
// synchronization is needed and there is no host/guest TSC offset.
func (c *vCPU) setSystemTime() error {
	return nil
}

// setSignalMask sets the vCPU signal mask.
//
// This must be called prior to running the vCPU.
//
// In KVM, this uses KVM_SET_SIGNAL_MASK ioctl to configure which
// signals are blocked while the vCPU is running. In SlimVM, the
// kernel module manages the signal mask internally during
// _SLIMVM_RUN (blocking all signals except SIGKILL, SIGSTOP,
// SIG_BOUNCE, and SIGPROF), so no userspace configuration is needed.
func (c *vCPU) setSignalMask() error {
	return nil
}
