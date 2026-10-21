//go:build windows

// Measures pure pipe round-trip time to the Moqi launcher/backend, using the
// same framing and protobuf messages as the TSF DLL. No product changes.
//
// Important: the pipe handle is synchronous. The TSF DLL writes a request and
// only then starts reading the reply; if a ReadFile is already pending while
// WriteFile is issued, the write never completes. So requests are strictly
// serialized here (write -> blocking read), exactly like the DLL.
package main

import (
	"encoding/binary"
	"fmt"
	"os"
	"sort"
	"syscall"
	"time"
	"unsafe"

	pb "github.com/gaboolic/moqi-ime/proto"
	"google.golang.org/protobuf/proto"
)

// openPipe opens the launcher pipe exactly like the TSF DLL: synchronous
// CreateFile + SetNamedPipeHandleState(PIPE_READMODE_MESSAGE), raw handle I/O.
func openPipe(path string, overlapped bool) (syscall.Handle, error) {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return syscall.InvalidHandle, err
	}
	var flags uint32
	if overlapped {
		flags = 0x40000000 // FILE_FLAG_OVERLAPPED
	}
	h, err := syscall.CreateFile(p,
		syscall.GENERIC_READ|syscall.GENERIC_WRITE,
		0, nil, syscall.OPEN_EXISTING, flags, 0)
	if err != nil {
		return syscall.InvalidHandle, err
	}
	const pipeReadmodeMessage = 0x00000002
	mode := uint32(pipeReadmodeMessage)
	proc := syscall.NewLazyDLL("kernel32.dll").NewProc("SetNamedPipeHandleState")
	r1, _, e1 := proc.Call(uintptr(h), uintptr(unsafe.Pointer(&mode)), 0, 0)
	if r1 == 0 {
		syscall.CloseHandle(h)
		return syscall.InvalidHandle, fmt.Errorf("SetNamedPipeHandleState: %v", e1)
	}
	return h, nil
}

var (
	kernel32                = syscall.NewLazyDLL("kernel32.dll")
	procCreateEventW        = kernel32.NewProc("CreateEventW")
	procGetOverlappedResult = kernel32.NewProc("GetOverlappedResult")
)

func createEvent() (syscall.Handle, error) {
	r, _, e := procCreateEventW.Call(0, 1, 0, 0) // manual reset, not signaled
	if r == 0 {
		return syscall.InvalidHandle, e
	}
	return syscall.Handle(r), nil
}

func getOverlappedResult(h syscall.Handle, ov *syscall.Overlapped, n *uint32) error {
	r, _, e := procGetOverlappedResult.Call(uintptr(h),
		uintptr(unsafe.Pointer(ov)), uintptr(unsafe.Pointer(n)), 0)
	if r == 0 {
		return e
	}
	return nil
}

// writeAll mirrors the TSF DLL's overlapped write: start the write, then wait
// on the event with a bounded timeout.
func writeAll(h syscall.Handle, buf []byte, overlapped bool) (int, error) {
	var n uint32
	if !overlapped {
		err := syscall.WriteFile(h, buf, &n, nil)
		return int(n), err
	}
	ev, err := createEvent()
	if err != nil {
		return 0, err
	}
	defer syscall.CloseHandle(ev)
	ov := &syscall.Overlapped{HEvent: ev}
	err = syscall.WriteFile(h, buf, &n, ov)
	if err == syscall.ERROR_IO_PENDING {
		if _, werr := syscall.WaitForSingleObject(ev, 5000); werr != nil {
			return 0, werr
		}
		if oerr := getOverlappedResult(h, ov, &n); oerr != nil {
			return int(n), oerr
		}
		return int(n), nil
	}
	return int(n), err
}

// readMessage mirrors the TSF DLL's overlapped read: start the read, wait on
// the event with the reply timeout, and cancel on timeout.
func readMessage(h syscall.Handle, buf []byte, overlapped bool) (int, error) {
	var n uint32
	if !overlapped {
		err := syscall.ReadFile(h, buf, &n, nil)
		return int(n), err
	}
	ev, err := createEvent()
	if err != nil {
		return 0, err
	}
	defer syscall.CloseHandle(ev)
	ov := &syscall.Overlapped{HEvent: ev}
	err = syscall.ReadFile(h, buf, &n, ov)
	if err == syscall.ERROR_IO_PENDING {
		if _, werr := syscall.WaitForSingleObject(ev, 8000); werr != nil {
			syscall.CancelIoEx(h, ov)
			syscall.WaitForSingleObject(ev, 5000)
			return 0, werr
		}
		if oerr := getOverlappedResult(h, ov, &n); oerr != nil {
			return int(n), oerr
		}
		return int(n), nil
	}
	return int(n), err
}

func main() {
	path := `\\.\pipe\TEST\MoqiIM\Launcher`
	if len(os.Args) > 1 {
		path = os.Args[1]
	}
	delayMs := 120
	if len(os.Args) > 2 {
		fmt.Sscanf(os.Args[2], "%d", &delayMs)
	}

	overlapped := os.Getenv("PROBE_OVERLAPPED") != ""
	h, err := openPipe(path, overlapped)
	if err != nil {
		fmt.Println("connect failed:", err)
		os.Exit(1)
	}
	defer syscall.CloseHandle(h)

	send := func(req *pb.ClientRequest) error {
		payload, err := proto.Marshal(req)
		if err != nil {
			return err
		}
		buf := make([]byte, 4+len(payload))
		binary.LittleEndian.PutUint32(buf, uint32(len(payload)))
		copy(buf[4:], payload)
		written, err := writeAll(h, buf, overlapped)
		if err != nil {
			return err
		}
		if written != len(buf) {
			return fmt.Errorf("short write: %d/%d", written, len(buf))
		}
		return nil
	}

	readBuf := make([]byte, 256*1024)
	readReply := func() (*pb.ServerResponse, error) {
		n, err := readMessage(h, readBuf, overlapped)
		if err != nil {
			return nil, err
		}
		data := readBuf[:n]
		if len(data) < 4 {
			return nil, fmt.Errorf("short frame: %d bytes", len(data))
		}
		size := binary.LittleEndian.Uint32(data[:4])
		if uint32(len(data)) < 4+size {
			return nil, fmt.Errorf("truncated frame: have %d, need %d", len(data)-4, size)
		}
		var resp pb.ServerResponse
		if err := proto.Unmarshal(data[4:4+size], &resp); err != nil {
			return nil, err
		}
		return &resp, nil
	}

	seq := uint32(0)
	pollMode := os.Getenv("PROBE_POLL") != ""
	peekNamedPipe := syscall.NewLazyDLL("kernel32.dll").NewProc("PeekNamedPipe")
	waitForData := func() {
		for {
			var avail uint32
			r, _, _ := peekNamedPipe.Call(uintptr(h), 0, 0, 0, uintptr(unsafe.Pointer(&avail)), 0)
			if r != 0 && avail > 0 {
				return
			}
			time.Sleep(2 * time.Millisecond) // RPC_REPLY_POLL_INTERVAL_MS
		}
	}
	call := func(req *pb.ClientRequest, timeout time.Duration) (*pb.ServerResponse, time.Duration) {
		seq++
		req.SeqNum = seq
		t0 := time.Now()
		if err := send(req); err != nil {
			fmt.Println("send failed:", err)
			os.Exit(1)
		}
		if pollMode {
			waitForData()
		}
		type result struct {
			resp *pb.ServerResponse
			err  error
		}
		ch := make(chan result, 1)
		go func() {
			resp, err := readReply()
			ch <- result{resp, err}
		}()
		select {
		case r := <-ch:
			if r.err != nil {
				fmt.Println("read failed:", r.err)
				os.Exit(1)
			}
			if r.resp.SeqNum != seq {
				fmt.Printf("unexpected seq: got %d want %d\n", r.resp.SeqNum, seq)
				os.Exit(1)
			}
			return r.resp, time.Since(t0)
		case <-time.After(timeout):
			fmt.Printf("TIMEOUT waiting seq=%d (%s)\n", seq, timeout)
			os.Exit(1)
		}
		return nil, 0
	}

	fmt.Println("pipe:", path)
	initReq := &pb.ClientRequest{
		Method:          pb.Method_METHOD_INIT,
		IsWindows8Above: true,
	}
	switch os.Getenv("PROBE_GUID") {
	case "none":
	case "bad":
		initReq.Guid = proto.String("{00000000-0000-0000-0000-000000000000}")
	default:
		initReq.Guid = proto.String("{3f6b5a12-8d44-4e71-9a2e-6b4f9c1d2a30}")
	}
	initResp, initDur := call(initReq, 180*time.Second)
	fmt.Printf("init: %s ret=%d client_id=%q\n",
		initDur.Round(time.Microsecond), initResp.ReturnValue, initResp.GetClientId())

	report := func(name string, vals []time.Duration, wall time.Duration) {
		if len(vals) == 0 {
			return
		}
		sort.Slice(vals, func(i, j int) bool { return vals[i] < vals[j] })
		var sum time.Duration
		over := 0
		for _, v := range vals {
			sum += v
			if v >= 30*time.Millisecond {
				over++
			}
		}
		p := func(q float64) time.Duration { return vals[int(q*float64(len(vals)-1))] }
		fmt.Printf("%-14s n=%3d mean=%7.3fms p50=%7.3f p90=%7.3f p99=%7.3f max=%7.3f >=30ms=%d wall=%s\n",
			name, len(vals), float64(sum.Microseconds())/float64(len(vals))/1000,
			float64(p(0.5).Microseconds())/1000,
			float64(p(0.9).Microseconds())/1000,
			float64(p(0.99).Microseconds())/1000,
			float64(vals[len(vals)-1].Microseconds())/1000,
			over, wall.Round(time.Millisecond))
	}

	n := 300
	pause := time.Duration(delayMs) * time.Millisecond
	var onVals, okVals, upVals, fdVals []time.Duration

	wall := time.Now()
	for i := 0; i < n; i++ {
		_, d := call(&pb.ClientRequest{
			Method:   pb.Method_METHOD_ON_KEY_DOWN,
			KeyEvent: &pb.KeyEvent{KeyCode: 'N', CharCode: 'n'},
		}, 10*time.Second)
		onVals = append(onVals, d)
		if pause > 0 {
			time.Sleep(pause)
		}
	}
	report("onKeyDown", onVals, time.Since(wall))

	wall = time.Now()
	for i := 0; i < n; i++ {
		_, d := call(&pb.ClientRequest{
			Method:   pb.Method_METHOD_ON_KEY_UP,
			KeyEvent: &pb.KeyEvent{KeyCode: 'N', CharCode: 'n'},
		}, 10*time.Second)
		okVals = append(okVals, d)
		if pause > 0 {
			time.Sleep(pause)
		}
	}
	report("onKeyUp", okVals, time.Since(wall))

	wall = time.Now()
	for i := 0; i < n; i++ {
		_, d := call(&pb.ClientRequest{
			Method:   pb.Method_METHOD_FILTER_KEY_UP,
			KeyEvent: &pb.KeyEvent{KeyCode: 'N', CharCode: 'n'},
		}, 10*time.Second)
		upVals = append(upVals, d)
		if pause > 0 {
			time.Sleep(pause)
		}
	}
	report("filterKeyUp", upVals, time.Since(wall))

	// filterKeyDown with a real engine call: type letters then reset with Escape.
	wall = time.Now()
	words := []string{"nihao", "womingtian", "qu", "beijing"}
	for round := 0; round < 6; round++ {
		for _, w := range words {
			for _, ch := range w {
				_, d := call(&pb.ClientRequest{
					Method:   pb.Method_METHOD_FILTER_KEY_DOWN,
					KeyEvent: &pb.KeyEvent{KeyCode: uint32(ch - 32), CharCode: uint32(ch)},
				}, 10*time.Second)
				fdVals = append(fdVals, d)
				if pause > 0 {
					time.Sleep(pause)
				}
			}
			// clear composition with Escape (avoid writing to the user's dictionary)
			call(&pb.ClientRequest{
				Method:   pb.Method_METHOD_FILTER_KEY_DOWN,
				KeyEvent: &pb.KeyEvent{KeyCode: 0x1B, CharCode: 0x1B},
			}, 10*time.Second)
			if pause > 0 {
				time.Sleep(pause)
			}
		}
	}
	report("filterKeyDown", fdVals, time.Since(wall))
}
