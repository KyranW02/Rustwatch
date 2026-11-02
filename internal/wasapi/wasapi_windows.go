//go:build windows

package wasapi

import (
	"encoding/binary"
	"errors"
	"fmt"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

var (
	ole32               = syscall.NewLazyDLL("ole32.dll")
	procCoInitializeEx  = ole32.NewProc("CoInitializeEx")
	procCoUninitialize  = ole32.NewProc("CoUninitialize")
	procCoCreateInstace = ole32.NewProc("CoCreateInstance")
	procCoTaskMemFree   = ole32.NewProc("CoTaskMemFree")
	mmdevapi            = syscall.NewLazyDLL("mmdevapi.dll")
	procActivateAsync   = mmdevapi.NewProc("ActivateAudioInterfaceAsync")
	kernel32            = syscall.NewLazyDLL("kernel32.dll")
	procCreateEventW    = kernel32.NewProc("CreateEventW")
	procWaitForSingle   = kernel32.NewProc("WaitForSingleObject")
)

type guid struct {
	D1     uint32
	D2, D3 uint16
	D4     [8]byte
}

func mustGUID(s string) guid {
	s = strings.Trim(s, "{}")
	p := strings.Split(s, "-")
	a, _ := strconv.ParseUint(p[0], 16, 32)
	b, _ := strconv.ParseUint(p[1], 16, 16)
	c, _ := strconv.ParseUint(p[2], 16, 16)
	g := guid{D1: uint32(a), D2: uint16(b), D3: uint16(c)}
	rest := p[3] + p[4]
	for i := 0; i < 8; i++ {
		v, _ := strconv.ParseUint(rest[i*2:i*2+2], 16, 8)
		g.D4[i] = byte(v)
	}
	return g
}

var (
	clsidMMDeviceEnumerator = mustGUID("BCDE0395-E52F-467C-8E3D-C4579291692E")
	iidIMMDeviceEnumerator  = mustGUID("A95664D2-9614-4F35-A746-DE8DB63617E6")
	iidIAudioClient         = mustGUID("1CB9AD4C-DBFA-4C32-B178-C2F568A703B2")
	iidIAudioCaptureClient  = mustGUID("C8ADBD64-E71E-48A0-A4DE-185C395CD317")
	iidCompletionHandler    = mustGUID("41D949AB-9862-444A-80F6-C261334DA5EB")
	iidIAgileObject         = mustGUID("94EA2B94-E9CC-49E0-C0FF-EE64CA8F5B90")
	iidIUnknown             = mustGUID("00000000-0000-0000-C000-000000000046")
	subtypeFloat            = mustGUID("00000003-0000-0010-8000-00AA00389B71")
)

const (
	clsctxAll             = 0x17
	streamLoopback        = 0x00020000
	streamEventCallback   = 0x00040000
	streamAutoConvertPCM  = 0x80000000
	streamSrcDefaultQual  = 0x08000000
	bufferFlagsSilent     = 0x2
	waveFormatPCM         = 1
	waveFormatIEEEFloat   = 3
	waveFormatExtensible  = 0xFFFE
	activationTypeProcess = 1
)

// ptrOf turns a uintptr returned by Windows into a pointer without tripping vet.
func ptrOf(p uintptr) unsafe.Pointer { return *(*unsafe.Pointer)(unsafe.Pointer(&p)) }

// vcall calls method idx of a COM object.
func vcall(obj uintptr, idx int, args ...uintptr) uintptr {
	vt := *(*unsafe.Pointer)(ptrOf(obj))
	fn := *(*uintptr)(unsafe.Add(vt, idx*int(unsafe.Sizeof(uintptr(0)))))
	r, _, _ := syscall.SyscallN(fn, append([]uintptr{obj}, args...)...)
	return r
}

func release(obj uintptr) {
	if obj != 0 {
		vcall(obj, 2)
	}
}

func hrErr(what string, hr uintptr) error {
	if int32(hr) >= 0 {
		return nil
	}
	return fmt.Errorf("%s failed (0x%08X)", what, uint32(hr))
}

// --- the completion handler ActivateAudioInterfaceAsync calls back into ---

type handlerVtbl struct{ qi, addRef, release, completed uintptr }

type handler struct {
	vtbl *handlerVtbl
	id   uintptr
}

var (
	handlerMu   sync.Mutex
	handlerDone = map[uintptr]chan struct{}{}
	handlerSeq  uintptr
	hVtbl       = &handlerVtbl{
		qi: syscall.NewCallback(func(this, riid, ppv uintptr) uintptr {
			g := *(*guid)(ptrOf(riid))
			if g == iidIUnknown || g == iidIAgileObject || g == iidCompletionHandler {
				*(*uintptr)(ptrOf(ppv)) = this
				return 0
			}
			*(*uintptr)(ptrOf(ppv)) = 0
			return 0x80004002 // E_NOINTERFACE
		}),
		addRef:  syscall.NewCallback(func(this uintptr) uintptr { return 2 }),
		release: syscall.NewCallback(func(this uintptr) uintptr { return 1 }),
		completed: syscall.NewCallback(func(this, op uintptr) uintptr {
			h := (*handler)(ptrOf(this))
			handlerMu.Lock()
			ch := handlerDone[h.id]
			handlerMu.Unlock()
			if ch != nil {
				select {
				case ch <- struct{}{}:
				default:
				}
			}
			return 0
		}),
	}
)

type propVariantBlob struct {
	vt         uint16
	r1, r2, r3 uint16
	cb         uint32
	_          uint32
	data       uintptr
}

type activationParams struct {
	Type uint32
	PID  uint32
	Mode uint32 // 0 = include the process and its children
}

// processClient gets an IAudioClient that records only pid's audio.
func processClient(pid uint32) (uintptr, error) {
	if err := procActivateAsync.Find(); err != nil {
		return 0, err
	}
	var pin runtime.Pinner
	defer pin.Unpin()
	params := &activationParams{Type: activationTypeProcess, PID: pid}
	pin.Pin(params)
	pv := &propVariantBlob{vt: 65 /* VT_BLOB */, cb: uint32(unsafe.Sizeof(*params)), data: uintptr(unsafe.Pointer(params))}
	pin.Pin(pv)
	h := &handler{vtbl: hVtbl}
	pin.Pin(h)
	done := make(chan struct{}, 1)
	handlerMu.Lock()
	handlerSeq++
	h.id = handlerSeq
	handlerDone[h.id] = done
	handlerMu.Unlock()
	defer func() { handlerMu.Lock(); delete(handlerDone, h.id); handlerMu.Unlock() }()
	path, _ := syscall.UTF16PtrFromString(`VAD\Process_Loopback`)
	iid := &iidIAudioClient
	op := new(uintptr)
	pin.Pin(op)
	hr, _, _ := procActivateAsync.Call(uintptr(unsafe.Pointer(path)), uintptr(unsafe.Pointer(iid)),
		uintptr(unsafe.Pointer(pv)), uintptr(unsafe.Pointer(h)), uintptr(unsafe.Pointer(op)))
	if err := hrErr("ActivateAudioInterfaceAsync", hr); err != nil {
		return 0, err
	}
	defer release(*op)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		return 0, errors.New("Windows didn't finish setting up process audio capture")
	}
	actHR := new(int32)
	unk := new(uintptr)
	pin.Pin(actHR)
	pin.Pin(unk)
	vcall(*op, 3, uintptr(unsafe.Pointer(actHR)), uintptr(unsafe.Pointer(unk)))
	if err := hrErr("process audio capture", uintptr(uint32(*actHR))); err != nil {
		return 0, err
	}
	return *unk, nil
}

// systemClient gets an IAudioClient on the default speakers.
func systemClient() (uintptr, error) {
	var pin runtime.Pinner
	defer pin.Unpin()
	enum, dev, cli := new(uintptr), new(uintptr), new(uintptr)
	pin.Pin(enum)
	pin.Pin(dev)
	pin.Pin(cli)
	hr, _, _ := procCoCreateInstace.Call(uintptr(unsafe.Pointer(&clsidMMDeviceEnumerator)), 0, clsctxAll,
		uintptr(unsafe.Pointer(&iidIMMDeviceEnumerator)), uintptr(unsafe.Pointer(enum)))
	if err := hrErr("MMDeviceEnumerator", hr); err != nil {
		return 0, err
	}
	defer release(*enum)
	if err := hrErr("GetDefaultAudioEndpoint", vcall(*enum, 4, 0 /* eRender */, 0 /* eConsole */, uintptr(unsafe.Pointer(dev)))); err != nil {
		return 0, err
	}
	defer release(*dev)
	if err := hrErr("IMMDevice.Activate", vcall(*dev, 3, uintptr(unsafe.Pointer(&iidIAudioClient)), clsctxAll, 0, uintptr(unsafe.Pointer(cli)))); err != nil {
		return 0, err
	}
	return *cli, nil
}

type format struct {
	rate, ch int
	float    bool
	bits     int
}

// waveFormat builds a WAVEFORMATEX for 16-bit PCM.
func pcm16(rate, ch int) []byte {
	b := make([]byte, 18)
	binary.LittleEndian.PutUint16(b[0:], waveFormatPCM)
	binary.LittleEndian.PutUint16(b[2:], uint16(ch))
	binary.LittleEndian.PutUint32(b[4:], uint32(rate))
	binary.LittleEndian.PutUint32(b[8:], uint32(rate*ch*2))
	binary.LittleEndian.PutUint16(b[12:], uint16(ch*2))
	binary.LittleEndian.PutUint16(b[14:], 16)
	return b
}

func parseFormat(p uintptr) (format, error) {
	b := unsafe.Slice((*byte)(ptrOf(p)), 40)
	tag := binary.LittleEndian.Uint16(b[0:])
	f := format{ch: int(binary.LittleEndian.Uint16(b[2:])), rate: int(binary.LittleEndian.Uint32(b[4:])), bits: int(binary.LittleEndian.Uint16(b[14:]))}
	switch tag {
	case waveFormatIEEEFloat:
		f.float = true
	case waveFormatPCM:
	case waveFormatExtensible:
		var g guid
		g.D1 = binary.LittleEndian.Uint32(b[24:])
		g.D2 = binary.LittleEndian.Uint16(b[28:])
		g.D3 = binary.LittleEndian.Uint16(b[30:])
		copy(g.D4[:], b[32:40])
		f.float = g == subtypeFloat
	default:
		return f, fmt.Errorf("unsupported audio format %d", tag)
	}
	if !f.float && f.bits != 16 && f.bits != 32 && f.bits != 24 {
		return f, fmt.Errorf("unsupported %d-bit audio", f.bits)
	}
	return f, nil
}

// run records from an initialised-capable client until stop closes or an error occurs.
func run(cli uintptr, mode Mode, stop <-chan struct{}, on OnAudio, started func(Mode)) error {
	defer release(cli)
	var pin runtime.Pinner
	defer pin.Unpin()
	var f format
	var fmtPtr uintptr
	flags := uintptr(streamLoopback)
	if mode == ModeProcess {
		wf := pcm16(44100, 2) // process loopback has no mix format; ask for this and let Windows convert
		pin.Pin(&wf[0])
		fmtPtr = uintptr(unsafe.Pointer(&wf[0]))
		f = format{rate: 44100, ch: 2, bits: 16}
		flags |= streamEventCallback | streamAutoConvertPCM | streamSrcDefaultQual
	} else {
		mp := new(uintptr)
		pin.Pin(mp)
		if err := hrErr("GetMixFormat", vcall(cli, 8, uintptr(unsafe.Pointer(mp)))); err != nil {
			return err
		}
		defer procCoTaskMemFree.Call(*mp)
		var err error
		if f, err = parseFormat(*mp); err != nil {
			return err
		}
		fmtPtr = *mp
	}
	const bufDur = 2000000 // 200 ms in 100-ns units
	if err := hrErr("IAudioClient.Initialize", vcall(cli, 3, 0 /* shared */, flags, bufDur, 0, fmtPtr, 0)); err != nil {
		return err
	}
	ev, _, _ := procCreateEventW.Call(0, 0, 0, 0)
	if ev != 0 {
		defer syscall.CloseHandle(syscall.Handle(ev))
		if flags&streamEventCallback != 0 {
			vcall(cli, 13, ev) // SetEventHandle
		}
	}
	capc := new(uintptr)
	pin.Pin(capc)
	if err := hrErr("GetService(capture)", vcall(cli, 14, uintptr(unsafe.Pointer(&iidIAudioCaptureClient)), uintptr(unsafe.Pointer(capc)))); err != nil {
		return err
	}
	defer release(*capc)
	if err := hrErr("IAudioClient.Start", vcall(cli, 10)); err != nil {
		return err
	}
	defer vcall(cli, 11) // Stop
	if started != nil {
		started(mode)
	}
	pkt, data, frames, bflags := new(uint32), new(uintptr), new(uint32), new(uint32)
	pin.Pin(pkt)
	pin.Pin(data)
	pin.Pin(frames)
	pin.Pin(bflags)
	var buf []float32
	for {
		select {
		case <-stop:
			return nil
		default:
		}
		if ev != 0 && flags&streamEventCallback != 0 {
			procWaitForSingle.Call(ev, 50)
		} else {
			time.Sleep(15 * time.Millisecond)
		}
		for {
			if err := hrErr("GetNextPacketSize", vcall(*capc, 5, uintptr(unsafe.Pointer(pkt)))); err != nil {
				return err
			}
			if *pkt == 0 {
				break
			}
			if err := hrErr("GetBuffer", vcall(*capc, 3, uintptr(unsafe.Pointer(data)), uintptr(unsafe.Pointer(frames)), uintptr(unsafe.Pointer(bflags)), 0, 0)); err != nil {
				return err
			}
			n := int(*frames) * f.ch
			buf = buf[:0]
			if *bflags&bufferFlagsSilent != 0 || *data == 0 {
				for i := 0; i < n; i++ {
					buf = append(buf, 0)
				}
			} else {
				switch {
				case f.float:
					buf = append(buf, unsafe.Slice((*float32)(ptrOf(*data)), n)...)
				case f.bits == 16:
					for _, v := range unsafe.Slice((*int16)(ptrOf(*data)), n) {
						buf = append(buf, float32(v)/32768)
					}
				case f.bits == 32:
					for _, v := range unsafe.Slice((*int32)(ptrOf(*data)), n) {
						buf = append(buf, float32(v)/2147483648)
					}
				case f.bits == 24:
					raw := unsafe.Slice((*byte)(ptrOf(*data)), n*3)
					for i := 0; i < n; i++ {
						v := int32(raw[i*3])<<8 | int32(raw[i*3+1])<<16 | int32(raw[i*3+2])<<24
						buf = append(buf, float32(v)/2147483648)
					}
				}
			}
			vcall(*capc, 4, uintptr(*frames)) // ReleaseBuffer
			if len(buf) > 0 {
				on(buf, f.rate, f.ch)
			}
		}
	}
}

// Capture records pid's audio (or, if that isn't supported and allowSystem is set, all PC
// audio) until stop closes. started is called once recording begins, with the mode in use.
func Capture(stop <-chan struct{}, pid uint32, allowSystem bool, on OnAudio, started func(Mode)) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	procCoInitializeEx.Call(0, 0 /* COINIT_MULTITHREADED */)
	defer procCoUninitialize.Call()

	cli, perr := processClient(pid)
	if perr == nil {
		began := false
		err := run(cli, ModeProcess, stop, on, func(m Mode) {
			began = true
			if started != nil {
				started(m)
			}
		})
		if err == nil || began || !allowSystem {
			return err // once Rust's own audio was recording, never widen to the whole PC
		}
		perr = err
	}
	if !allowSystem {
		return perr
	}
	cli, err := systemClient()
	if err != nil {
		return fmt.Errorf("%v; all-PC audio: %v", perr, err)
	}
	return run(cli, ModeSystem, stop, on, started)
}
