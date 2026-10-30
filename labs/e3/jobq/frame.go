package jobq

import "io"

// MaxFrame is the largest payload a frame may carry. A length field above it
// can only be corruption, and must not make recovery allocate gigabytes.
const MaxFrame = 1 << 20

// FrameHeader is the size of a frame header: a 4-byte big-endian payload
// length followed by the 4-byte big-endian CRC-32C (Castagnoli) of the payload.
const FrameHeader = 8

// AppendFrame writes one frame (header + payload) to w in a single Write call,
// so a process crash can never leave half a frame behind (a power loss or a
// full disk still can; that is what ScanFrames is for). It rejects an empty
// payload and one larger than MaxFrame.
//
// Milestone 1. Use hash/crc32 with crc32.MakeTable(crc32.Castagnoli) and
// encoding/binary.BigEndian.
func AppendFrame(w io.Writer, payload []byte) error {
	// TODO(lab): build header+payload in one buffer and write it once.
	return errNotImplemented
}

// ScanFrames reads frames from r until EOF or the first frame that is torn
// (header or payload cut short), oversized (length 0 or above MaxFrame) or
// corrupt (CRC mismatch). It returns the payloads of the good frames in order
// and good, the byte offset just past the last good frame: the caller
// truncates the file there before appending again. err is reserved for read
// errors other than a short read; a torn or corrupt tail is not an error.
//
// Milestone 1. io.ReadFull distinguishes io.EOF (nothing left) from
// io.ErrUnexpectedEOF (cut short); both end the good prefix here.
func ScanFrames(r io.Reader) (frames [][]byte, good int64, err error) {
	// TODO(lab): read header, validate length, read payload, check CRC, repeat.
	return nil, 0, nil
}
