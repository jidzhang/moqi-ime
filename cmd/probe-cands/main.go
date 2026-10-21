//go:build windows

// Prints the candidate list the running Moqi backend returns for a short
// composition, so the effective "total candidate count" (appearance config
// candidate_count) can be verified end-to-end without the TSF front end.
//
// Usage: probe-cands.exe [pipeName] [pinyin]
package main

import (
	"encoding/binary"
	"fmt"
	"os"
	"syscall"
	"time"
	"unsafe"

	"google.golang.org/protobuf/proto"

	pb "github.com/gaboolic/moqi-ime/proto"
)

func openPipe(path string) (syscall.Handle, error) {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return syscall.InvalidHandle, err
	}
	h, err := syscall.CreateFile(p,
		syscall.GENERIC_READ|syscall.GENERIC_WRITE,
		0, nil, syscall.OPEN_EXISTING, 0, 0)
	if err != nil {
		return syscall.InvalidHandle, err
	}
	const pipeReadmodeMessage = 0x00000002
	mode := uint32(pipeReadmodeMessage)
	proc := syscall.NewLazyDLL("kernel32.dll").NewProc("SetNamedPipeHandleState")
	if r1, _, e1 := proc.Call(uintptr(h), uintptr(unsafe.Pointer(&mode)), 0, 0); r1 == 0 {
		syscall.CloseHandle(h)
		return syscall.InvalidHandle, fmt.Errorf("SetNamedPipeHandleState: %v", e1)
	}
	return h, nil
}

func main() {
	path := `\\.\pipe\TEST\MoqiIM\Launcher`
	if len(os.Args) > 1 {
		path = os.Args[1]
	}
	text := "nihao"
	if len(os.Args) > 2 {
		text = os.Args[2]
	}

	h, err := openPipe(path)
	if err != nil {
		fmt.Println("connect failed:", err)
		os.Exit(1)
	}
	defer syscall.CloseHandle(h)

	seq := uint32(0)
	readBuf := make([]byte, 256*1024)
	call := func(req *pb.ClientRequest) *pb.ServerResponse {
		seq++
		req.SeqNum = seq
		payload, err := proto.Marshal(req)
		if err != nil {
			panic(err)
		}
		buf := make([]byte, 4+len(payload))
		binary.LittleEndian.PutUint32(buf, uint32(len(payload)))
		copy(buf[4:], payload)
		var written uint32
		if err := syscall.WriteFile(h, buf, &written, nil); err != nil {
			fmt.Println("write failed:", err)
			os.Exit(1)
		}
		var n uint32
		if err := syscall.ReadFile(h, readBuf, &n, nil); err != nil {
			fmt.Println("read failed:", err)
			os.Exit(1)
		}
		data := readBuf[:n]
		size := binary.LittleEndian.Uint32(data[:4])
		var resp pb.ServerResponse
		if err := proto.Unmarshal(data[4:4+size], &resp); err != nil {
			fmt.Println("unmarshal failed:", err)
			os.Exit(1)
		}
		return &resp
	}

	call(&pb.ClientRequest{
		Method:          pb.Method_METHOD_INIT,
		IsWindows8Above: true,
		Guid:            proto.String("{3f6b5a12-8d44-4e71-9a2e-6b4f9c1d2a30}"),
	})
	call(&pb.ClientRequest{Method: pb.Method_METHOD_ON_ACTIVATE})

	for _, ch := range text {
		call(&pb.ClientRequest{
			Method:   pb.Method_METHOD_FILTER_KEY_DOWN,
			KeyEvent: &pb.KeyEvent{KeyCode: uint32(ch - 32), CharCode: uint32(ch)},
		})
		time.Sleep(20 * time.Millisecond)
	}
	resp := call(&pb.ClientRequest{Method: pb.Method_METHOD_ON_KEY_DOWN})

	fmt.Printf("composition=%q candidates=%d\n", resp.CompositionString, len(resp.CandidateList))
	for i, c := range resp.CandidateList {
		fmt.Printf("  [%d] %q\n", i+1, c)
	}
}
