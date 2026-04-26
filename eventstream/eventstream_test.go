package eventstream

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"io"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	frame := Encode(map[string]string{
		":event-type":   "contentBlockDelta",
		":content-type": "application/json",
	}, []byte(`{"delta":{"text":"hi"}}`))
	d := NewDecoder(bytes.NewReader(frame))
	got, err := d.Next()
	if err != nil {
		t.Fatal(err)
	}
	if got.Get(":event-type") != "contentBlockDelta" {
		t.Errorf("event-type: %v", got.Headers)
	}
	if string(got.Payload) != `{"delta":{"text":"hi"}}` {
		t.Errorf("payload: %s", got.Payload)
	}
	if _, err := d.Next(); err != io.EOF {
		t.Errorf("expected EOF, got %v", err)
	}
}

func TestMultipleFrames(t *testing.T) {
	a := Encode(map[string]string{":event-type": "a"}, []byte("AAA"))
	b := Encode(map[string]string{":event-type": "b"}, []byte("BB"))
	d := NewDecoder(bytes.NewReader(append(a, b...)))
	f1, err := d.Next()
	if err != nil || f1.Get(":event-type") != "a" {
		t.Fatalf("frame 1: %v %v", f1, err)
	}
	f2, err := d.Next()
	if err != nil || f2.Get(":event-type") != "b" {
		t.Fatalf("frame 2: %v %v", f2, err)
	}
	if _, err := d.Next(); err != io.EOF {
		t.Errorf("eof: %v", err)
	}
}

func TestEmpty(t *testing.T) {
	d := NewDecoder(bytes.NewReader(nil))
	if _, err := d.Next(); err != io.EOF {
		t.Errorf("expected EOF, got %v", err)
	}
}

func TestTruncatedPrelude(t *testing.T) {
	d := NewDecoder(bytes.NewReader([]byte{0, 0, 0}))
	if _, err := d.Next(); err != ErrTruncated {
		t.Errorf("got %v", err)
	}
}

func TestPreludeCRCMismatch(t *testing.T) {
	frame := Encode(map[string]string{":x": "y"}, []byte("hi"))
	frame[8] ^= 0xff // corrupt prelude CRC
	if _, err := NewDecoder(bytes.NewReader(frame)).Next(); err == nil {
		t.Error("expected CRC mismatch error")
	}
}

func TestMessageCRCMismatch(t *testing.T) {
	frame := Encode(map[string]string{":x": "y"}, []byte("hi"))
	frame[len(frame)-1] ^= 0xff
	if _, err := NewDecoder(bytes.NewReader(frame)).Next(); err == nil {
		t.Error("expected message CRC error")
	}
}

func TestInvalidLengths(t *testing.T) {
	// totalLen < 16
	var buf [12]byte
	binary.BigEndian.PutUint32(buf[0:4], 8)
	binary.BigEndian.PutUint32(buf[4:8], 0)
	binary.BigEndian.PutUint32(buf[8:12], crc32.Checksum(buf[0:8], crc32.IEEETable))
	if _, err := NewDecoder(bytes.NewReader(buf[:])).Next(); err == nil {
		t.Error("expected invalid lengths error")
	}
}

func TestHeadersLenOverflow(t *testing.T) {
	var buf [12]byte
	binary.BigEndian.PutUint32(buf[0:4], 16)   // total = 16, body slot = 4
	binary.BigEndian.PutUint32(buf[4:8], 1000) // headers > total-16
	binary.BigEndian.PutUint32(buf[8:12], crc32.Checksum(buf[0:8], crc32.IEEETable))
	if _, err := NewDecoder(bytes.NewReader(buf[:])).Next(); err == nil {
		t.Error("expected invalid lengths")
	}
}

func TestTruncatedBody(t *testing.T) {
	frame := Encode(map[string]string{":x": "y"}, []byte("hello"))
	if _, err := NewDecoder(bytes.NewReader(frame[:len(frame)-3])).Next(); err != ErrTruncated {
		t.Errorf("got %v", err)
	}
}

type fakeReader struct{ err error }

func (f fakeReader) Read([]byte) (int, error) { return 0, f.err }

func TestPreludeReadError(t *testing.T) {
	d := NewDecoder(fakeReader{err: errors.New("boom")})
	if _, err := d.Next(); err == nil {
		t.Error("expected read error")
	}
}

// readerSeq reads from a sequence of readers, then errors with custom err on the 2nd ReadFull.
type readerSeq struct {
	first  []byte
	off    int
	second error
}

func (r *readerSeq) Read(p []byte) (int, error) {
	if r.off < len(r.first) {
		n := copy(p, r.first[r.off:])
		r.off += n
		return n, nil
	}
	return 0, r.second
}

func TestBodyReadError(t *testing.T) {
	frame := Encode(map[string]string{":x": "y"}, []byte("hi"))
	r := &readerSeq{first: frame[:12], second: errors.New("body fail")}
	if _, err := NewDecoder(r).Next(); err == nil {
		t.Error("expected body read error")
	}
}

func TestUnsupportedHeaderType(t *testing.T) {
	// Build a frame manually with header type = 0 (bool true), unsupported here.
	hdr := []byte{1, 'x', 0} // name "x" type 0
	totalLen := uint32(12 + len(hdr) + 0 + 4)
	out := make([]byte, 0, totalLen)
	var tmp [4]byte
	binary.BigEndian.PutUint32(tmp[:], totalLen)
	out = append(out, tmp[:]...)
	binary.BigEndian.PutUint32(tmp[:], uint32(len(hdr)))
	out = append(out, tmp[:]...)
	binary.BigEndian.PutUint32(tmp[:], crc32.Checksum(out, crc32.IEEETable))
	out = append(out, tmp[:]...)
	out = append(out, hdr...)
	binary.BigEndian.PutUint32(tmp[:], crc32.Checksum(out, crc32.IEEETable))
	out = append(out, tmp[:]...)
	if _, err := NewDecoder(bytes.NewReader(out)).Next(); err == nil {
		t.Error("expected unsupported type error")
	}
}

func TestTruncatedHeaderFields(t *testing.T) {
	// All the various decodeHeaders truncation paths.
	cases := [][]byte{
		{},                     // ok actually -> empty headers; replace below
		{1},                    // truncated name
		{1, 'x'},               // truncated type
		{1, 'x', 7},            // truncated string length
		{1, 'x', 7, 0, 5, 'a'}, // truncated string value
	}
	// Build a frame with given header bytes, expect error for non-empty malformed cases.
	for i, hdr := range cases {
		if i == 0 {
			continue
		}
		out := mkRawFrame(t, hdr, nil)
		if _, err := NewDecoder(bytes.NewReader(out)).Next(); err == nil {
			t.Errorf("case %d: expected error", i)
		}
	}
}

func mkRawFrame(t *testing.T, hdr, payload []byte) []byte {
	t.Helper()
	totalLen := uint32(12 + len(hdr) + len(payload) + 4)
	out := make([]byte, 0, totalLen)
	var tmp [4]byte
	binary.BigEndian.PutUint32(tmp[:], totalLen)
	out = append(out, tmp[:]...)
	binary.BigEndian.PutUint32(tmp[:], uint32(len(hdr)))
	out = append(out, tmp[:]...)
	binary.BigEndian.PutUint32(tmp[:], crc32.Checksum(out, crc32.IEEETable))
	out = append(out, tmp[:]...)
	out = append(out, hdr...)
	out = append(out, payload...)
	binary.BigEndian.PutUint32(tmp[:], crc32.Checksum(out, crc32.IEEETable))
	out = append(out, tmp[:]...)
	return out
}
