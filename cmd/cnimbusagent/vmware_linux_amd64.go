//go:build linux && amd64

// vmwareFetch reads a "guestinfo.<key>" variable set on the host (a
// guestinfo.* line in the VM's .vmx file, or e.g. `govc vm.change -e
// guestinfo.<key>=<value>`) through VMware's own "backdoor" I/O
// protocol -- the same low-bandwidth RPCI wire format open-vm-tools'
// own lib/message/message.c implements. Reconstructed here against that
// public source (github.com/vmware/open-vm-tools), specifically:
//
//   - lib/include/backdoor_def.h: BDOOR_MAGIC (0x564D5868, "VMXh" in
//     ASCII), BDOOR_PORT (0x5658), BDOOR_CMD_MESSAGE (30).
//   - lib/include/guest_msg_def.h: the MessageType enum (OPEN=0,
//     SENDSIZE=1, SENDPAYLOAD=2, RECVSIZE=3, RECVPAYLOAD=4,
//     RECVSTATUS=5, CLOSE=6) and MESSAGE_STATUS_* reply bits.
//   - lib/include/rpcout.h: RPCI_PROTOCOL_NUM (0x49435052, "RPCI").
//   - lib/message/message.c (Message_OpenAllocated/Send/Receive/Close)
//     and lib/backdoor/backdoor.c (Backdoor()): the exact register
//     layout below (ax=magic, bx="size" -- meaning depends on the
//     call, cx=(msgType<<16)|BDOOR_CMD_MESSAGE, dx=(channelId<<16)|
//     BDOOR_PORT, si/di=cookie) and the fact that Backdoor() itself
//     ORs BDOOR_PORT into dx's low 16 bits right before the trap.
//   - lib/backdoor/backdoorGcc64.c (Backdoor_InOut): confirms the
//     actual instruction is `inl %%dx, %%eax` -- there is no 64-bit IN,
//     so only the low 32 bits of each register carry meaning.
//   - Verified against a real VMware Player VM (host-side `vmrun
//     writeVariable <vmx> guestVar message <value>`, guest-side
//     "info-get guestinfo.message"): the reply on the wire is "1
//     <value>" on success -- a leading status digit + space is the
//     vmx-side "info-get" handler's own application-level convention,
//     layered on top of the raw Message_Send/Receive channel itself
//     (which carries whatever bytes the handler chooses to send, no
//     framing of its own). rpcParseReply strips it back off before
//     handing the value to agentruntime, so this kind still honors
//     every other AGENT kind's "write the fetched value verbatim"
//     contract from the Nimbusfile author's point of view.
//
// One channel is opened, used for one "info-get guestinfo.<key>"
// request, and closed -- matching RpcOut_SendOneRaw's own per-command
// lifecycle rather than keeping a channel open across polls the way
// AGENT vboxguest does.
//
// Only the "low-bandwidth" 4-bytes-per-call path is implemented. The
// high-bandwidth port (0x5659, REP INSB/OUTSB) is a pure throughput
// optimization the protocol makes optional (see message.c's
// MESSAGE_STATUS_HB branch) -- not needed for the small values a KV
// agent moves, and skipping it avoids a second, riskier asm routine.
// This also doesn't implement the checkpoint-retry (MESSAGE_STATUS_CPT)
// dance real vmtoolsd handles: a failed fetch here just logs and waits
// for the next poll, same as every other AGENT kind.
//
// Requires I/O permission for port 0x5658 (BDOOR_PORT), granted once at
// process start via the ioperm(2) syscall -- this agent always runs as
// root inside the image. Executing IN/OUT on a port without permission
// faults (SIGSEGV). On real hardware or under a non-VMware hypervisor,
// the same IN instruction just addresses a real (almost certainly
// unmapped) I/O port -- harmless, but the result is garbage, which is
// why every step below checks MESSAGE_STATUS_SUCCESS rather than
// assuming a real answer came back.
//
// ioperm(0x5658, 4, 1), not iopl(3): the backdoorGcc64.c asm this file
// reimplements only ever drives the one dword-wide I/O instruction at
// BDOOR_PORT (see the asm helper's own doc comment) -- ioperm grants
// exactly that 4-byte port range, where iopl(3) would have granted
// this process unrestricted access to all 65536 I/O ports.
package main

import (
	"encoding/binary"
	"fmt"
	"strings"
	"sync"

	"golang.org/x/sys/unix"
)

const (
	bdoorMagic    = 0x564D5868
	bdoorPort     = 0x5658
	bdoorCmdMsg   = 30
	rpciProto     = 0x49435052 // "RPCI"
	msgFlagCookie = 0x80000000

	msgTypeOpen        = 0
	msgTypeSendSize    = 1
	msgTypeSendPayload = 2
	msgTypeRecvSize    = 3
	msgTypeRecvPayload = 4
	msgTypeRecvStatus  = 5
	msgTypeClose       = 6

	msgStatusSuccess = 0x0001
	msgStatusDoRecv  = 0x0002
)

// backdoorCall is implemented in vmware_linux_amd64.s.
func backdoorCall(regs *[6]uint64)

var (
	iopermOnce sync.Once
	iopermErr  error
)

func ensureIOPerm() error {
	iopermOnce.Do(func() {
		if err := unix.Ioperm(bdoorPort, 4, 1); err != nil {
			iopermErr = fmt.Errorf("ioperm(0x%x, 4): %w (this agent must run as root)", bdoorPort, err)
		}
	})
	return iopermErr
}

func vmwareFetch(key string) (func() ([]byte, error), error) {
	if err := ensureIOPerm(); err != nil {
		return nil, err
	}
	request := "info-get guestinfo." + key
	return func() ([]byte, error) {
		reply, err := rpciSendOneRaw(request)
		if err != nil {
			return nil, err
		}
		value, err := parseInfoGetReply(reply)
		if err != nil {
			return nil, fmt.Errorf("vmware: guestinfo.%s: %w", key, err)
		}
		return []byte(value), nil
	}, nil
}

// parseInfoGetReply strips "info-get"'s own leading status digit ("1 "
// on success, "0" -- with or without a trailing error message -- when
// the variable isn't set) off an RPCI reply. This convention belongs to
// the vmx-side "info-get" command handler, not the Message_Send/Receive
// channel itself (which carries whatever bytes a handler chooses to
// send); see this file's own doc comment for how this was verified
// against a real VM.
func parseInfoGetReply(reply string) (string, error) {
	status, value, ok := strings.Cut(reply, " ")
	if !ok {
		status, value = reply, ""
	}
	if status != "1" {
		return "", fmt.Errorf("not set (or empty) -- reply: %q", reply)
	}
	return value, nil
}

// backdoorOp issues one low-bandwidth backdoor call and returns the six
// registers afterward. channel/cookieHi/cookieLo are 0 for the OPEN
// call, which doesn't have a channel yet.
func backdoorOp(msgType, channel, cookieHi, cookieLo uint32, size uint64) [6]uint64 {
	r := [6]uint64{
		bdoorMagic,
		size,
		uint64(bdoorCmdMsg) | uint64(msgType)<<16,
		uint64(channel)<<16 | bdoorPort,
		uint64(cookieHi),
		uint64(cookieLo),
	}
	backdoorCall(&r)
	return r
}

func status(r [6]uint64) uint32 { return uint32(r[2] >> 16) }

// rpciSendOneRaw opens a fresh RPCI channel, sends one request string,
// reads back the (possibly empty) reply, and closes the channel --
// mirroring open-vm-tools' RpcOut_SendOneRaw.
func rpciSendOneRaw(request string) (string, error) {
	r := backdoorOp(msgTypeOpen, 0, 0, 0, uint64(rpciProto)|msgFlagCookie)
	if status(r)&msgStatusSuccess == 0 {
		return "", fmt.Errorf("vmware rpci: open failed -- not running under VMware, or the backdoor is blocked?")
	}
	channel := uint32(r[3] >> 16)
	cookieHi := uint32(r[4])
	cookieLo := uint32(r[5])
	defer backdoorOp(msgTypeClose, channel, cookieHi, cookieLo, 0)

	r = backdoorOp(msgTypeSendSize, channel, cookieHi, cookieLo, uint64(len(request)))
	if status(r)&msgStatusSuccess == 0 {
		return "", fmt.Errorf("vmware rpci: sendsize failed")
	}

	buf := []byte(request)
	for len(buf) > 0 {
		n := min(4, len(buf))
		var word [4]byte
		copy(word[:], buf[:n])
		r = backdoorOp(msgTypeSendPayload, channel, cookieHi, cookieLo, uint64(binary.LittleEndian.Uint32(word[:])))
		if status(r)&msgStatusSuccess == 0 {
			return "", fmt.Errorf("vmware rpci: sendpayload failed")
		}
		buf = buf[n:]
	}

	r = backdoorOp(msgTypeRecvSize, channel, cookieHi, cookieLo, 0)
	if status(r)&msgStatusSuccess == 0 {
		return "", fmt.Errorf("vmware rpci: recvsize failed")
	}
	if status(r)&msgStatusDoRecv == 0 {
		return "", nil // vmware has nothing to say back -- a successful, empty reply
	}
	remaining := uint32(r[1])

	reply := make([]byte, 0, remaining)
	for remaining > 0 {
		r = backdoorOp(msgTypeRecvPayload, channel, cookieHi, cookieLo, msgStatusSuccess)
		if status(r)&msgStatusSuccess == 0 {
			return "", fmt.Errorf("vmware rpci: recvpayload failed")
		}
		var word [4]byte
		binary.LittleEndian.PutUint32(word[:], uint32(r[1]))
		n := min(4, int(remaining))
		reply = append(reply, word[:n]...)
		remaining -= uint32(n)
	}

	r = backdoorOp(msgTypeRecvStatus, channel, cookieHi, cookieLo, msgStatusSuccess)
	if status(r)&msgStatusSuccess == 0 {
		return "", fmt.Errorf("vmware rpci: recvstatus failed")
	}

	return string(reply), nil
}
