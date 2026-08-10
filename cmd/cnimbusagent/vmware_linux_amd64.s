#include "textflag.h"

// func backdoorCall(regs *[6]uint64)
//
// regs holds, in order, the six general registers VMware's backdoor
// protocol reads on entry and rewrites on exit around a single `IN EAX,
// DX` (opcode 0xED, encoded directly since Go's assembler has no IN/OUT
// mnemonic): ax, bx, cx, dx, si, di. Real hardware or any non-VMware
// hypervisor only ever changes EAX; under VMware, this specific I/O
// port access (0x5658, loaded into DX by the caller) is trapped by the
// host, which is what makes the other five registers meaningful on
// return -- see vmware_linux_amd64.go's doc comment for the full
// protocol writeup. R8 holds the *regs pointer across the IN so it
// survives even though AX (the argument register on entry) does not.
TEXT ·backdoorCall(SB), NOSPLIT, $0-8
	MOVQ regs+0(FP), R8
	MOVQ 40(R8), DI
	MOVQ 32(R8), SI
	MOVQ 24(R8), DX
	MOVQ 16(R8), CX
	MOVQ 8(R8), BX
	MOVQ 0(R8), AX
	BYTE $0xed // IN EAX, DX
	MOVQ AX, 0(R8)
	MOVQ BX, 8(R8)
	MOVQ CX, 16(R8)
	MOVQ DX, 24(R8)
	MOVQ SI, 32(R8)
	MOVQ DI, 40(R8)
	RET
