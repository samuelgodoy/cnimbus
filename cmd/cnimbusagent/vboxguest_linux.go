//go:build linux

// vboxguestFetch talks to VirtualBox's real native guest-integration
// channel -- the same one Guest Additions uses -- without installing
// Guest Additions. That's possible because Oracle upstreamed the
// VBoxGuest driver into mainline Linux itself (drivers/virt/vboxguest/,
// since ~4.14): enabling CONFIG_VBOXGUEST in the kernel we already
// build from source gives us /dev/vboxguest and its documented ioctl
// interface (include/uapi/linux/vboxguest.h) for free, no out-of-tree
// module, no VBoxService daemon.
//
// What VBoxGuest's ioctls don't include is the Guest Properties
// service itself -- that's an HGCM ("Host-Guest Communication
// Manager") service named "VBoxGuestPropSvc", implemented on the host
// side inside VirtualBox, with its own small wire protocol
// (VBox/HostServices/GuestPropertySvc.h in VirtualBox's own source,
// not the Linux kernel tree): function 1 is GET_PROP, taking a
// property name and returning its value, and that's the only function
// this agent actually needs.
//
// Connects to VBoxGuestPropSvc once, returning a closure that polls one
// property name on each call and hands back its value verbatim -- the
// exact same "whatever bytes came back, unmodified" contract every
// other AGENT kind writes. Earlier versions of this agent wrapped the
// value in a hardcoded {"message": "<value>"} envelope, which was the
// one AGENT kind that didn't just write through the raw fetched
// content -- if you want a JSON shape your own ENTRYPOINT/SERVICE reads
// a specific key from, set the Guest Property's value to that JSON
// yourself (e.g. `VBoxManage guestproperty set <vm> <prop>
// '{"message":"hi"}'`); this agent no longer imposes one for you.
package main

import (
	"encoding/binary"
	"fmt"
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	devPath = "/dev/vboxguest"

	vbgIoctlHdrVersion = 0x10001

	hgcmLocLocalhost = 1 // enum vmmdev_hgcm_service_location_type

	parmType32Bit      = 1
	parmType64Bit      = 2
	parmTypeLinAddrIn  = 5
	parmTypeLinAddrOut = 6

	guestPropSvcName   = "VBoxGuestPropSvc"
	guestPropFnGetProp = 1

	outBufSize = 4096
)

func ioctlNr(dir, typ, nr uint32, size uintptr) uintptr {
	const (
		nrShift   = 0
		typeShift = 8
		sizeShift = 16
		dirShift  = 30
		sizeMask  = 0x3FFF
	)
	return uintptr(dir<<dirShift | typ<<typeShift | nr<<nrShift | (uint32(size)&sizeMask)<<sizeShift) // #nosec G115 -- size is always one of this file's own small fixed struct sizes, well under uint32 range
}

// putHdr writes a vbg_ioctl_hdr (24 bytes) at the start of buf.
func putHdr(buf []byte, sizeIn, sizeOut uint32) {
	binary.LittleEndian.PutUint32(buf[0:4], sizeIn)
	binary.LittleEndian.PutUint32(buf[4:8], vbgIoctlHdrVersion)
	binary.LittleEndian.PutUint32(buf[8:12], 0)  // type: VBG_IOCTL_HDR_TYPE_DEFAULT
	binary.LittleEndian.PutUint32(buf[12:16], 0) // rc, out only
	binary.LittleEndian.PutUint32(buf[16:20], sizeOut)
	binary.LittleEndian.PutUint32(buf[20:24], 0) // reserved
}

func doIoctl(fd int, req uintptr, buf []byte) error {
	_, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), req, uintptr(unsafe.Pointer(&buf[0]))) // #nosec G103 -- this IS the ioctl(2) call the /dev/vboxguest ABI requires; there's no non-unsafe way to invoke it
	if errno != 0 {
		return errno
	}
	return nil
}

// hgcmConnect connects to the named HGCM service and returns its
// client ID. Mirrors struct vbg_ioctl_hgcm_connect (156 bytes: 24-byte
// header + a 132-byte vmmdev_hgcm_service_location).
func hgcmConnect(fd int, service string) (uint32, error) {
	// vbg_ioctl_chk (drivers/virt/vboxguest/vboxguest_core.c) requires
	// size_out to equal header+sizeof(union.out) EXACTLY -- 0 does not
	// mean "same as size_in" here despite the header field's own doc
	// comment describing that shorthand for some other ioctl; verified
	// empirically ("invalid argument" until this matched precisely).
	// union.out is just { u32 client_id }, so size_out = 24 + 4 = 28.
	const size = 24 + 132
	const sizeOut = 24 + 4
	buf := make([]byte, size)
	putHdr(buf, size, sizeOut)
	binary.LittleEndian.PutUint32(buf[24:28], hgcmLocLocalhost)
	copy(buf[28:28+128], service) // zero-padded; Go's copy stops at len(service)

	req := ioctlNr(3, 'V', 4, size) // VBG_IOCTL_HGCM_CONNECT = _IOWR('V', 4, ...)
	if err := doIoctl(fd, req, buf); err != nil {
		return 0, fmt.Errorf("HGCM_CONNECT to %s: %w", service, err)
	}
	if rc := int32(binary.LittleEndian.Uint32(buf[12:16])); rc < 0 { // #nosec G115 -- VBoxGuestPropSvc's rc is documented as a signed int32 the host writes into this u32 slot; reinterpreting is the protocol, not a truncation risk
		return 0, fmt.Errorf("HGCM_CONNECT to %s: host rc=%d", service, rc)
	}
	return binary.LittleEndian.Uint32(buf[24:28]), nil
}

// hgcmGetProp calls VBoxGuestPropSvc's GET_PROP (function 1): 4
// parameters (name in, buffer out, timestamp out u64, size out u32),
// each a 16-byte vmmdev_hgcm_function_parameter64. Mirrors
// GuestPropMsgGetProperty from VirtualBox's own GuestPropertySvc.h.
func hgcmGetProp(fd int, clientID uint32, name string) (string, error) {
	nameBuf := append([]byte(name), 0)
	valBuf := make([]byte, outBufSize)

	const (
		hdrSize  = 24
		callHdr  = 16 // client_id, function, timeout_ms, interruptible, reserved, parm_count
		parmSize = 16
		numParms = 4
	)
	size := hdrSize + callHdr + numParms*parmSize
	buf := make([]byte, size)
	putHdr(buf, uint32(size), uint32(size))

	binary.LittleEndian.PutUint32(buf[24:28], clientID)
	binary.LittleEndian.PutUint32(buf[28:32], guestPropFnGetProp)
	binary.LittleEndian.PutUint32(buf[32:36], 10000) // timeout_ms
	buf[36] = 0                                      // interruptible
	buf[37] = 0                                      // reserved
	binary.LittleEndian.PutUint16(buf[38:40], numParms)

	p := 40
	putLinAddrParm(buf[p:p+parmSize], parmTypeLinAddrIn, nameBuf)
	p += parmSize
	putLinAddrParm(buf[p:p+parmSize], parmTypeLinAddrOut, valBuf)
	p += parmSize
	put64Parm(buf[p:p+parmSize], 0) // timestamp, out
	p += parmSize
	sizeParmOffset := p             // remember where the "size out" u32 parameter lands, to read the host's real answer back below
	put32Parm(buf[p:p+parmSize], 0) // size, out

	req := ioctlNr(3, 'V', 7, uintptr(size)) // VBG_IOCTL_HGCM_CALL_64 = _IOC(RW,'V',7,...)
	if err := doIoctl(fd, req, buf); err != nil {
		return "", fmt.Errorf("HGCM_CALL GET_PROP %s: %w", name, err)
	}
	if rc := int32(binary.LittleEndian.Uint32(buf[12:16])); rc < 0 { // #nosec G115 -- VBoxGuestPropSvc's rc is documented as a signed int32 the host writes into this u32 slot; reinterpreting is the protocol, not a truncation risk
		return "", fmt.Errorf("HGCM_CALL GET_PROP %s: host rc=%d", name, rc)
	}

	// The host writes the property's real total size (value+flags, as a
	// u32) back into this parameter's value field -- previously never
	// read, so a value bigger than outBufSize was silently truncated
	// with no way to tell. put32Parm lays out {type u32}{value u32}, so
	// the value itself is 4 bytes past where the parameter starts.
	realSize := binary.LittleEndian.Uint32(buf[sizeParmOffset+4 : sizeParmOffset+8])
	if int(realSize) > outBufSize {
		return "", fmt.Errorf("HGCM_CALL GET_PROP %s: value is %d bytes, exceeding this agent's %d-byte buffer -- truncated, refusing to return a partial value", name, realSize, outBufSize)
	}

	// valBuf holds "value\0flags\0" -- only the value (up to the first
	// NUL) is wanted here.
	for i, b := range valBuf {
		if b == 0 {
			return string(valBuf[:i]), nil
		}
	}
	return string(valBuf), nil
}

func putLinAddrParm(dst []byte, parmType uint32, data []byte) {
	binary.LittleEndian.PutUint32(dst[0:4], parmType)
	binary.LittleEndian.PutUint32(dst[4:8], uint32(len(data)))                          // #nosec G115 -- data is always this file's own small fixed-size buffer (nameBuf/valBuf), never attacker-sized
	binary.LittleEndian.PutUint64(dst[8:16], uint64(uintptr(unsafe.Pointer(&data[0])))) // #nosec G103 -- passing a buffer address to the host via HGCM's LinAddr parameter type is exactly what this ioctl ABI requires
}

func put64Parm(dst []byte, v uint64) {
	binary.LittleEndian.PutUint32(dst[0:4], parmType64Bit)
	binary.LittleEndian.PutUint64(dst[4:12], v)
}

func put32Parm(dst []byte, v uint32) {
	binary.LittleEndian.PutUint32(dst[0:4], parmType32Bit)
	binary.LittleEndian.PutUint32(dst[4:8], v)
}

// vboxGuestFetch opens /dev/vboxguest and connects to VBoxGuestPropSvc
// once; the returned closure polls property on each call.
func vboxGuestFetch(property string) (func() ([]byte, error), error) {
	fd, err := unix.Open(devPath, unix.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", devPath, err)
	}
	clientID, err := hgcmConnect(fd, guestPropSvcName)
	if err != nil {
		_ = unix.Close(fd)
		return nil, err
	}
	return func() ([]byte, error) {
		value, err := hgcmGetProp(fd, clientID, property)
		if err != nil {
			return nil, err
		}
		return []byte(value), nil
	}, nil
}
