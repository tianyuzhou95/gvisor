// Copyright 2026 The gVisor Authors.
// SPDX-License-Identifier: Apache-2.0

package tcp

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"testing"
	"time"

	"gvisor.dev/gvisor/pkg/state"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/ports"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/waiter"
)

// TestLiveSaveTCPOutputOrdering is a bounded component regression, not a
// complete workload checkpoint. It uses real IPTables.beforeSave (via the
// serializer), a real TCP Endpoint and timerHandler, and Stack/protocol.Resume.
// The timer is dispatched while serialization holds conntrack;
// natural keepalive cadence and a full Kernel.SaveTo are separate E2E gates.
// No packet is sent to a host or interface: CheckOutput runs only in memory.
//
// Children contain a broken implementation's blocked goroutines. The parent
// always reaps them. Both callbacks must remain pending while paused and
// complete after Resume, including output through the saved conntrack table.
func TestLiveSaveTCPOutputOrdering(t *testing.T) {
	if mode := os.Getenv("GVISOR_LIVE_SAVE_TIMER_CHILD"); mode != "" {
		runLiveSaveTCPOutputChild(t, mode == "output")
		return
	}
	for _, mode := range []string{"control", "output"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			binary, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.CommandContext(ctx, binary, "-test.run=^TestLiveSaveTCPOutputOrdering$", "-test.v")
			cmd.Env = append(os.Environ(), "GVISOR_LIVE_SAVE_TIMER_CHILD="+mode)
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("source Resume did not complete (%s): %v\n%s", mode, err, output)
			}
		})
	}
}

func runLiveSaveTCPOutputChild(t *testing.T, output bool) {
	// ConnTrack serializes its clock. Channel barriers select the interleaving,
	// so use the savable standard clock rather than advancing fake time.
	clock := tcpip.NewStdClock()
	s := stack.New(stack.Options{
		Clock:              clock,
		NetworkProtocols:   []stack.NetworkProtocolFactory{ipv4.NewProtocol},
		TransportProtocols: []stack.TransportProtocolFactory{NewProtocol},
	})
	p := s.TransportProtocolInstance(ProtocolNumber).(*protocol)
	ep := newEndpoint(s, p, header.IPv4ProtocolNumber, &waiter.Queue{})
	ep.ID = stack.TransportEndpointID{LocalPort: 12345}
	if err := s.RegisterTransportEndpoint([]tcpip.NetworkProtocolNumber{header.IPv4ProtocolNumber}, ProtocolNumber, ep.ID, ep, ports.Flags{}, 0); err != nil {
		t.Fatal(err)
	}
	ep.mu.Lock()
	ep.setEndpointState(StateTimeWait)
	startTimeWait(ep)
	ep.mu.Unlock()
	s.Pause()
	if len(p.pausedTimeWaitEndpoints) != 1 || p.pausedTimeWaitEndpoints[0] != ep {
		t.Fatal("test did not pause its exact endpoint")
	}

	tables := s.IPTables()
	tables.ForceReplaceTable(stack.FilterID, tables.GetTable(stack.FilterID, false), false)
	var image bytes.Buffer
	if _, err := state.Save(context.Background(), &image, tables); err != nil {
		t.Fatal(err)
	}
	// Serialization has returned, leaving the source's actual C write lock
	// held until source Resume calls IPTables.resumeAfterSave.
	entered := make(chan struct{})
	callbackDone := make(chan struct{})
	handler := timerHandler(ep, func() tcpip.Error {
		close(entered) // Real timerHandler currently owns this exact E.
		if output {
			pkt := stack.NewPacketBuffer(stack.PacketBufferOptions{
				ReserveHeaderBytes: header.IPv4MinimumSize + header.TCPMinimumSize,
			})
			defer pkt.DecRef()
			header.TCP(pkt.TransportHeader().Push(header.TCPMinimumSize)).Encode(&header.TCPFields{
				SrcPort: 12345, DstPort: 80, DataOffset: header.TCPMinimumSize,
				Flags: header.TCPFlagSyn, WindowSize: 65535,
			})
			pkt.TransportProtocolNumber = header.TCPProtocolNumber
			header.IPv4(pkt.NetworkHeader().Push(header.IPv4MinimumSize)).Encode(&header.IPv4Fields{
				TotalLength: header.IPv4MinimumSize + header.TCPMinimumSize,
				TTL:         64, Protocol: uint8(header.TCPProtocolNumber),
				SrcAddr: tcpip.AddrFrom4([4]byte{192, 0, 2, 1}),
				DstAddr: tcpip.AddrFrom4([4]byte{198, 51, 100, 2}),
			})
			pkt.NetworkProtocolNumber = header.IPv4ProtocolNumber
			tables.CheckOutput(pkt, nil, "") // Requests actual C.RLock under E.
		}
		return nil
	})
	clock.AfterFunc(0, func() {
		handler()
		close(callbackDone)
	})
	// A timer which fires during serialization must not acquire E or start
	// output. It remains pending until Resume opens callback admission.
	select {
	case <-entered:
		t.Fatal("timer callback entered while stack was paused")
	case <-time.After(20 * time.Millisecond):
	}
	resumed := make(chan struct{})
	go func() {
		s.Resume() // On the old source: wait for E before releasing C.
		close(resumed)
	}()
	select {
	case <-resumed:
	case <-time.After(2 * time.Second):
		buf := make([]byte, 1<<20)
		n := runtime.Stack(buf, true)
		fmt.Fprintf(os.Stderr, "source Resume deadline; output=%t\n%s", output, buf[:n])
		os.Exit(86) // No blocked cleanup or goroutine leak in the parent.
	}
	select {
	case <-callbackDone:
	case <-time.After(time.Second):
		os.Exit(87)
	}
	// This synthetic endpoint has no peer route. Finish its normal TIME_WAIT
	// cleanup before Stack.Destroy can Abort it and try to send a reset.
	ep.timeWaitTimerExpired()
	if got := ep.EndpointState(); got != StateClose {
		t.Fatalf("fixture TIME_WAIT cleanup: got %v, want %v", got, StateClose)
	}
	ep.Close()
	// Registration above used the stack API directly, without endpoint-owned
	// registration bookkeeping, so remove that exact entry explicitly.
	s.UnregisterTransportEndpoint([]tcpip.NetworkProtocolNumber{header.IPv4ProtocolNumber}, ProtocolNumber, ep.ID, ep, ports.Flags{}, 0)
	s.Destroy()
}

// A save may fail before IPTables.beforeSave is visited. Unwind must not
// unlock an unowned mutex, and an explicit coordinated thaw must be idempotent.
func TestSourceResumeWithoutConntrackSave(t *testing.T) {
	s := stack.New(stack.Options{TransportProtocols: []stack.TransportProtocolFactory{NewProtocol}})
	defer s.Destroy()
	for i := 0; i < 3; i++ {
		s.Pause()
		if i != 0 {
			var image bytes.Buffer
			if _, err := state.Save(context.Background(), &image, s.IPTables()); err != nil {
				t.Fatal(err)
			}
		}
		s.PrepareResume()
		s.PrepareResume()
		s.Resume()
	}
}

func TestPauseDrainsAdmittedTCPTimer(t *testing.T) {
	s := stack.New(stack.Options{TransportProtocols: []stack.TransportProtocolFactory{NewProtocol}})
	defer s.Destroy()
	p := s.TransportProtocolInstance(ProtocolNumber).(*protocol)
	ep := newEndpoint(s, p, header.IPv4ProtocolNumber, &waiter.Queue{})
	defer ep.Close()
	entered, release, callbackDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
	handler := timerHandler(ep, func() tcpip.Error { close(entered); <-release; return nil })
	go func() { handler(); close(callbackDone) }()
	<-entered
	paused := make(chan struct{})
	go func() { s.Pause(); close(paused) }()
	select {
	case <-paused:
		t.Fatal("Pause returned while an admitted callback was running")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	select {
	case <-paused:
	case <-time.After(time.Second):
		t.Fatal("Pause did not drain the callback")
	}
	<-callbackDone
	s.Resume()
}

// Timers may deliver output into another network namespace. That namespace's
// conntrack must be thawed even if its protocol producers have not resumed yet.
func TestResumeAcrossStacks(t *testing.T) {
	var stacks []*stack.Stack
	for i := 0; i < 2; i++ {
		s := stack.New(stack.Options{
			NetworkProtocols:   []stack.NetworkProtocolFactory{ipv4.NewProtocol},
			TransportProtocols: []stack.TransportProtocolFactory{NewProtocol},
		})
		defer s.Destroy()
		stacks = append(stacks, s)
		s.Pause()
		tables := s.IPTables()
		tables.ForceReplaceTable(stack.FilterID, tables.GetTable(stack.FilterID, false), false)
		var image bytes.Buffer
		if _, err := state.Save(context.Background(), &image, tables); err != nil {
			t.Fatal(err)
		}
	}
	s := stacks[0]
	ep := newEndpoint(s, s.TransportProtocolInstance(ProtocolNumber).(*protocol), header.IPv4ProtocolNumber, &waiter.Queue{})
	defer ep.Close()
	done := make(chan struct{})
	handler := timerHandler(ep, func() tcpip.Error {
		pkt := stack.NewPacketBuffer(stack.PacketBufferOptions{
			ReserveHeaderBytes: header.IPv4MinimumSize + header.TCPMinimumSize,
		})
		defer pkt.DecRef()
		header.TCP(pkt.TransportHeader().Push(header.TCPMinimumSize)).Encode(&header.TCPFields{
			SrcPort: 12345, DstPort: 80, DataOffset: header.TCPMinimumSize, Flags: header.TCPFlagSyn,
		})
		pkt.TransportProtocolNumber = header.TCPProtocolNumber
		header.IPv4(pkt.NetworkHeader().Push(header.IPv4MinimumSize)).Encode(&header.IPv4Fields{
			TotalLength: header.IPv4MinimumSize + header.TCPMinimumSize,
			TTL:         64, Protocol: uint8(header.TCPProtocolNumber),
			SrcAddr: tcpip.AddrFrom4([4]byte{192, 0, 2, 1}),
			DstAddr: tcpip.AddrFrom4([4]byte{198, 51, 100, 2}),
		})
		pkt.NetworkProtocolNumber = header.IPv4ProtocolNumber
		stacks[1].IPTables().CheckOutput(pkt, nil, "")
		return nil
	})
	go func() { handler(); close(done) }()
	for _, s := range stacks {
		s.PrepareResume()
	}
	stacks[0].Resume()
	select {
	case <-done:
	case <-time.After(time.Second):
		// Thaw the peer before failing so cleanup cannot hang on the timer.
		stacks[1].Resume()
		<-done
		t.Fatal("source timer blocked on a namespace whose producers are still paused")
	}
	stacks[1].Resume()
}
