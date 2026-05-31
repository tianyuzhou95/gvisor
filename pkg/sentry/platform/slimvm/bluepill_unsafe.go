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
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
	"gvisor.dev/gvisor/pkg/sentry/arch"
)

var (
	// Memory reserved for triggering OOM in HR3 when slimvm returns ENOMEM
	dummyBytes []byte

	// We will try to recover when OOM happened only if we have at least
	// 100MB memory available.
	nrDummyBytes = 100 << 20
)

//go:linkname throw runtime.throw
func throw(string)

var hexSyms = []byte("0123456789abcdef")

// printHex writes title followed by val (as a 16-digit hex number) to stderr
// using only a raw write(2). It is async-signal-safe: it allocates nothing,
// takes no lock, and may run on the signal stack.
//
//go:nosplit
func printHex(title []byte, val uint64) {
	var str [19]byte
	str[0] = ' '
	for i := 0; i < 16; i++ {
		str[16-i] = hexSyms[val&0xf]
		val = val >> 4
	}
	str[17] = '\n'
	syscall.RawSyscall(syscall.SYS_WRITE, uintptr(unix.Stderr), uintptr(unsafe.Pointer(&title[0])), uintptr(len(title)))
	syscall.RawSyscall(syscall.SYS_WRITE, uintptr(unix.Stderr), uintptr(unsafe.Pointer(&str[0])), 18)
}

// bluepillDieCleanly reports a fatal condition encountered inside
// bluepillHandler and terminates the process.
//
// It must be used instead of throw() from within the signal handler context:
// runtime.throw acquires the runtime print lock (debuglock), which is not
// async-signal-safe and deadlocks when reached from the bluepill SIGSEGV
// handler (see the BenchmarkApplicationSyscall hang). This helper only issues
// raw system calls: it writes a diagnostic line to stderr and then exits the
// whole process with status 99.
//
//go:nosplit
func bluepillDieCleanly(msg []byte, status uint64) {
	syscall.RawSyscall(syscall.SYS_WRITE, uintptr(unix.Stderr), uintptr(unsafe.Pointer(&msg[0])), uintptr(len(msg)))
	printHex(dieStatusTitle, status)
	syscall.RawSyscall(syscall.SYS_EXIT_GROUP, 99, 0, 0)
}

var dieStatusTitle = []byte("slimvm: fatal in bluepillHandler, status =")

var (
	dieMsgInvalidState     = []byte("slimvm: invalid vCPU state in bluepillHandler\n")
	dieMsgPendingSignal    = []byte("slimvm: error waiting for pending signal\n")
	dieMsgUnexpectedSignal = []byte("slimvm: unexpected signal\n")
	dieMsgNMIInjection     = []byte("slimvm: NMI injection failed\n")
	dieMsgOOMRelease       = []byte("slimvm: OOM: failed to release dummy bytes\n")
	dieMsgOOMTime          = []byte("slimvm: OOM: failed to get current time\n")
	dieMsgOOMRepeat        = []byte("slimvm: OOM: ENOMEM happened more than 5 times on this vCPU in last minute\n")
	dieMsgRunFailed        = []byte("slimvm: run ioctl failed\n")
	dieMsgException        = []byte("slimvm: unexpected exception exit\n")
	dieMsgIO               = []byte("slimvm: unexpected I/O exit\n")
	dieMsgHypercall        = []byte("slimvm: unexpected hypercall exit\n")
	dieMsgDebug            = []byte("slimvm: unexpected debug exit\n")
	dieMsgMMIO             = []byte("slimvm: VM exit MMIO, maybe a physical address is out of range\n")
	dieMsgShutdown         = []byte("slimvm: unexpected shutdown exit\n")
	dieMsgFailEntry        = []byte("slimvm: VM entry failed\n")
	dieMsgMSRWrite         = []byte("slimvm: write msr failed\n")
	dieMsgUnknown          = []byte("slimvm: unknown VM exit status\n")
)

// vCPUPtr returns a CPU for the given address.
//
//go:nosplit
func vCPUPtr(addr uintptr) *vCPU {
	return (*vCPU)(unsafe.Pointer(addr))
}

// bytePtr returns a bytePtr for the given address.
//
//go:nosplit
func bytePtr(addr uintptr) *byte {
	return (*byte)(unsafe.Pointer(addr))
}

// uintptrValue returns a uintptr for the given address.
//
//go:nosplit
func uintptrValue(addr *byte) uintptr {
	return (uintptr)(unsafe.Pointer(addr))
}

// bluepillArchContext returns the arch-specific context.
//
//go:nosplit
func bluepillArchContext(context unsafe.Pointer) *arch.SignalContext64 {
	return &((*arch.UContext64)(context).MContext)
}

// bluepillHandler is called from the signal stub.
//
// The world may be stopped while this is executing, and it executes on the
// signal stack. It should only execute raw system calls and functions that are
// explicitly marked go:nosplit.
//
//go:nosplit
func bluepillHandler(context unsafe.Pointer) {
	// Sanitize the registers; interrupts must always be disabled.
	c := bluepillArchEnter(bluepillArchContext(context))

	// Mark this as guest mode.
	switch c.state.Swap(vCPUGuest | vCPUUser) {
	case vCPUUser: // Expected case.
	case vCPUUser | vCPUWaiter:
		c.notify()
	default:
		bluepillDieCleanly(dieMsgInvalidState, uint64(c.state.Load()))
	}

	for {
		switch _, _, errno := syscall.RawSyscall(syscall.SYS_IOCTL, slimvmFD, _SLIMVM_RUN, uintptr(unsafe.Pointer(&c.vmxConfig))); errno {
		case 0: // Expected case.
		case syscall.EINTR:
			// In SlimVM, bounce signals (SIG_BOUNCE) are consumed by
			// the kernel module in slimvm_signal_handler() and never
			// reach userspace. EINTR here is triggered by non-bounce
			// signals (e.g. SIGPROF).
			//
			// Use zero timeout so rt_sigtimedwait is non-blocking:
			// if there is no pending bounce signal, EAGAIN is returned
			// and we simply rerun the vCPU.
			timeout := unix.Timespec{}
			sig, _, errno := syscall.RawSyscall6(
				syscall.SYS_RT_SIGTIMEDWAIT,
				uintptr(unsafe.Pointer(&bounceSignalMask)),
				0,                                 // siginfo.
				uintptr(unsafe.Pointer(&timeout)), // zero timeout.
				8,                                 // sigset size.
				0, 0)
			if errno == unix.EAGAIN {
				continue
			}
			if errno != 0 {
				bluepillDieCleanly(dieMsgPendingSignal, uint64(errno))
			}
			if sig != uintptr(bounceSignal) {
				bluepillDieCleanly(dieMsgUnexpectedSignal, uint64(sig))
			}

			// Check whether the current state of the vCPU is ready
			// for interrupt injection. Because we don't have a
			// PIC, we can't inject an interrupt while they are
			// masked. We need to request a window if it's not
			// ready.
			if c.runData.readyForInterruptInjection == 0 {
				c.runData.requestInterruptWindow = 1
				continue // Rerun vCPU.
			} else {
				// Force injection below; the vCPU is ready.
				c.runData.exitReason = _SLIMVM_EXIT_IRQ_WINDOW_OPEN
			}
		case syscall.EFAULT:
			// If a fault is not serviceable due to the host
			// backing pages having page permissions, instead of an
			// MMIO exit we receive EFAULT from the run ioctl. We
			// always inject an NMI here since we may be in kernel
			// mode and have interrupts disabled.
			if _, _, errno := syscall.RawSyscall(
				syscall.SYS_IOCTL,
				slimvmFD,
				_SLIMVM_NMI, uintptr(unsafe.Pointer(&c.vmxConfig.vcpu))); errno != 0 {
				bluepillDieCleanly(dieMsgNMIInjection, uint64(errno))
			}
			continue // Rerun vCPU.
		case syscall.ENOMEM:
			// OOM happened. Trigger the OOM killer in HR3.
			for i := 0; i < nrDummyBytes; i += 4096 {
				dummyBytes[i] = 0xff
			}

			// We failed to trigger the OOM killer. It's possible that we have
			// the enough memory now. Release the dummy bytes and try again.
			if _, _, errno := syscall.RawSyscall(
				syscall.SYS_MADVISE,
				uintptr(unsafe.Pointer(&dummyBytes[0])),
				uintptr(nrDummyBytes),
				syscall.MADV_DONTNEED); errno != 0 {
				bluepillDieCleanly(dieMsgOOMRelease, uint64(errno))
			}

			var now syscall.Timeval
			if _, _, errno := syscall.RawSyscall(
				syscall.SYS_GETTIMEOFDAY,
				uintptr(unsafe.Pointer(&now)),
				0, 0); errno != 0 {
				bluepillDieCleanly(dieMsgOOMTime, uint64(errno))
			}

			c.OOMCount++
			elapsed := now.Sec - c.OOMLastTS
			if elapsed > 60 {
				// Reset the counter if it has expired (more than 60s).
				c.OOMCount = 1
				c.OOMLastTS = now.Sec
			} else if c.OOMCount > 5 {
				bluepillDieCleanly(dieMsgOOMRepeat, uint64(c.OOMCount))
			}
			continue // Rerun vCPU.
		default:
			bluepillDieCleanly(dieMsgRunFailed, uint64(errno))
		}

		switch c.vmxConfig.status {
		case _SLIMVM_EXIT_EXCEPTION:
			bluepillDieCleanly(dieMsgException, uint64(c.vmxConfig.status))
		case _SLIMVM_EXIT_IO:
			bluepillDieCleanly(dieMsgIO, uint64(c.vmxConfig.status))
		case _SLIMVM_EXIT_INTERNAL_ERROR:
			// An internal error is typically thrown when emulation
			// fails. This can occur via the MMIO path below (and
			// it might fail because we have multiple regions that
			// are not mapped). We would actually prefer that no
			// emulation occur, and don't mind at all if it fails.
		case _SLIMVM_EXIT_HYPERCALL:
			bluepillDieCleanly(dieMsgHypercall, uint64(c.vmxConfig.status))
		case _SLIMVM_EXIT_DEBUG:
			bluepillDieCleanly(dieMsgDebug, uint64(c.vmxConfig.status))
		case _SLIMVM_EXIT_HLT:
			// Increment our counter.
			c.guestExits.Add(1)

			// Copy out registers.
			bluepillArchExit(c, bluepillArchContext(context))

			// Return to the vCPUReady state; notify any waiters.
			user := c.state.Load() & vCPUUser
			switch c.state.Swap(user) {
			case user | vCPUGuest: // Expected case.
			case user | vCPUGuest | vCPUWaiter:
				c.notify()
			default:
				bluepillDieCleanly(dieMsgInvalidState, uint64(c.state.Load()))
			}
			return
		case _SLIMVM_EXIT_MMIO:
			bluepillDieCleanly(dieMsgMMIO, uint64(c.vmxConfig.status))
		case _SLIMVM_EXIT_IRQ_WINDOW_OPEN:
			// NOTE: KVM-residual code. In SlimVM, interrupt injection
			// (bounce, NMI) is handled entirely by the kernel module
			// via vmx_inject_bounce()/vmx_inject_nmi() before VM
			// entry. This exit reason should not occur in practice.
			// Clear previous injection request.
			c.runData.requestInterruptWindow = 0
		case _SLIMVM_EXIT_SHUTDOWN:
			bluepillDieCleanly(dieMsgShutdown, uint64(c.vmxConfig.status))
		case _SLIMVM_EXIT_FAIL_ENTRY:
			bluepillDieCleanly(dieMsgFailEntry, uint64(c.vmxConfig.status))
		case _SLIMVM_EXIT_INTR:
			/* Signal Handler */
		case _SLIMVM_EXIT_MSR_WRITE:
			bluepillDieCleanly(dieMsgMSRWrite, uint64(c.vmxConfig.status))
		default:
			bluepillDieCleanly(dieMsgUnknown, uint64(c.vmxConfig.status))
		}
	}
}

func init() {
	dummyBytes, _ = syscall.Mmap(-1, 0, nrDummyBytes, syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_ANONYMOUS|syscall.MAP_PRIVATE)
}
