package jobq

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
)

// MaxFrame is the largest payload a frame may carry. A length field above it
// can only be corruption, and must not make recovery allocate gigabytes.
const MaxFrame = 1 << 20

// FrameHeader is the size of a frame header: a 4-byte big-endian payload
// length followed by the 4-byte big-endian CRC-32C (Castagnoli) of the payload.
const FrameHeader = 8

var castagnoli = crc32.MakeTable(crc32.Castagnoli)

// AppendFrame writes one frame (header + payload) to w in a single Write call,
// so a process crash can never leave half a frame behind (a power loss or a
// full disk still can; that is what ScanFrames is for).
func AppendFrame(w io.Writer, payload []byte) error {
	if len(payload) == 0 || len(payload) > MaxFrame {
		return fmt.Errorf("frame payload must be 1..%d bytes, got %d", MaxFrame, len(payload))
	}
	buf := make([]byte, FrameHeader+len(payload))
	binary.BigEndian.PutUint32(buf[0:4], uint32(len(payload)))
	binary.BigEndian.PutUint32(buf[4:8], crc32.Checksum(payload, castagnoli))
	copy(buf[FrameHeader:], payload)
	_, err := w.Write(buf)
	return err
}

// ScanFrames reads frames from r until EOF or the first frame that is torn
// (header or payload cut short), oversized (length 0 or above MaxFrame) or
// corrupt (CRC mismatch). It returns the payloads of the good frames in order
// and good, the byte offset just past the last good frame: the caller
// truncates the file there before appending again. err is reserved for read
// errors other than a short read; a torn or corrupt tail is not an error.
func ScanFrames(r io.Reader) (frames [][]byte, good int64, err error) {
	br := bufio.NewReader(r)
	var hdr [FrameHeader]byte
	for {
		if _, err := io.ReadFull(br, hdr[:]); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return frames, good, nil
			}
			return frames, good, err
		}
		n := binary.BigEndian.Uint32(hdr[0:4])
		if n == 0 || n > MaxFrame {
			return frames, good, nil
		}
		payload := make([]byte, n)
		if _, err := io.ReadFull(br, payload); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return frames, good, nil
			}
			return frames, good, err
		}
		if crc32.Checksum(payload, castagnoli) != binary.BigEndian.Uint32(hdr[4:8]) {
			return frames, good, nil
		}
		frames = append(frames, payload)
		good += int64(FrameHeader) + int64(n)
	}
}
