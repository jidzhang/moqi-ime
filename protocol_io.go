package main

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"

	moqipb "github.com/gaboolic/moqi-ime/proto"
	"google.golang.org/protobuf/encoding/protojson"
)

func readFrame(reader *bufio.Reader) ([]byte, error) {
	var sizeBuf [4]byte
	if _, err := io.ReadFull(reader, sizeBuf[:]); err != nil {
		return nil, err
	}
	size := binary.LittleEndian.Uint32(sizeBuf[:])
	if size == 0 {
		return nil, fmt.Errorf("invalid empty frame")
	}
	payload := make([]byte, size)
	if _, err := io.ReadFull(reader, payload); err != nil {
		return nil, err
	}
	return payload, nil
}

func writeFrame(writer io.Writer, payload []byte) error {
	var sizeBuf [4]byte
	binary.LittleEndian.PutUint32(sizeBuf[:], uint32(len(payload)))
	if _, err := writer.Write(sizeBuf[:]); err != nil {
		return err
	}
	_, err := writer.Write(payload)
	return err
}

func decodeClientRequest(payload []byte) (*moqipb.ClientRequest, error) {
	msg := &moqipb.ClientRequest{}
	if err := protojson.Unmarshal(payload, msg); err != nil {
		return nil, err
	}
	return msg, nil
}

// protojson 默认省略零值字段（false/空串/空数组/0），而 DLL 的清组字/关候选
// 分支依赖这些显式键（showCandidates:false、空 candidateEntries、空
// compositionString、seqNum:0）——必须全量输出（Notepad4 双 ESC/候选窗不消失
// 的根因，2026-11-01 用户实报）。
var protojsonMarshal = protojson.MarshalOptions{EmitUnpopulated: true}

func encodeServerResponse(msg *moqipb.ServerResponse) ([]byte, error) {
	return protojsonMarshal.Marshal(msg)
}
