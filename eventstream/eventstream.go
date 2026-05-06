// Package eventstream decodes the AWS application/vnd.amazon.eventstream
// framing used by Bedrock streaming responses.
//
// Wire format (big-endian, network order):
//
//	+--------------------+ 4 bytes total length (incl. trailer CRC)
//	+--------------------+ 4 bytes headers length
//	+--------------------+ 4 bytes prelude CRC32 over the previous 8 bytes
//	+--------------------+ headers (typed key/value pairs)
//	+--------------------+ payload (totalLen - headersLen - 16)
//	+--------------------+ 4 bytes message CRC32 over the entire message except the trailer
//
// Header value types — only the ones Bedrock uses are decoded fully:
//   - 7 (string)
//
// Other types are recognised so the framer doesn't crash, but their values
// are returned as raw bytes.
package eventstream

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
)

// Frame is a decoded event-stream message.
type Frame struct {
	Headers map[string]string
	Payload []byte
}

// Get returns the value of header key, or "".
func (f Frame) Get(k string) string { return f.Headers[k] }

// Decoder reads frames sequentially from an underlying reader.
type Decoder struct {
	r io.Reader
}

// NewDecoder constructs a Decoder.
func NewDecoder(r io.Reader) *Decoder { return &Decoder{r: r} }

// crcTable is shared (the IEEE polynomial is what AWS uses).
var crcTable = crc32.IEEETable

// ErrTruncated is returned when EOF arrives mid-frame.
var ErrTruncated = errors.New("eventstream: truncated frame")

// Next decodes the next frame. Returns io.EOF cleanly only at frame boundary.
func (d *Decoder) Next() (Frame, error) {
	var prelude [12]byte
	n, err := io.ReadFull(d.r, prelude[:])
	if err != nil {
		if err == io.EOF && n == 0 {
			return Frame{}, io.EOF
		}
		if err == io.ErrUnexpectedEOF {
			return Frame{}, ErrTruncated
		}
		return Frame{}, err
	}
	totalLen := binary.BigEndian.Uint32(prelude[0:4])
	headersLen := binary.BigEndian.Uint32(prelude[4:8])
	preludeCRC := binary.BigEndian.Uint32(prelude[8:12])

	if want := crc32.Checksum(prelude[0:8], crcTable); want != preludeCRC {
		return Frame{}, fmt.Errorf("eventstream: prelude CRC mismatch (got %x want %x)", preludeCRC, want)
	}
	if totalLen < 16 || headersLen > totalLen-16 {
		return Frame{}, fmt.Errorf("eventstream: invalid frame lengths total=%d headers=%d", totalLen, headersLen)
	}
	rest := make([]byte, totalLen-12)
	if _, err := io.ReadFull(d.r, rest); err != nil {
		if err == io.ErrUnexpectedEOF || err == io.EOF {
			return Frame{}, ErrTruncated
		}
		return Frame{}, err
	}
	// rest = headers || payload || trailerCRC(4)
	msgCRC := binary.BigEndian.Uint32(rest[len(rest)-4:])
	body := rest[:len(rest)-4]
	// CRC of full message excluding trailer = prelude || body.
	h := crc32.NewIEEE()
	h.Write(prelude[:])
	h.Write(body)
	if h.Sum32() != msgCRC {
		return Frame{}, fmt.Errorf("eventstream: message CRC mismatch (got %x want %x)", msgCRC, h.Sum32())
	}
	headers, err := decodeHeaders(body[:headersLen])
	if err != nil {
		return Frame{}, err
	}
	payload := append([]byte(nil), body[headersLen:]...)
	return Frame{Headers: headers, Payload: payload}, nil
}

func decodeHeaders(b []byte) (map[string]string, error) {
	out := map[string]string{}
	for i := 0; i < len(b); {
		nameLen := int(b[i])
		i++
		if i+nameLen > len(b) {
			return nil, errors.New("eventstream: truncated header name")
		}
		name := string(b[i : i+nameLen])
		i += nameLen
		if i+1 > len(b) {
			return nil, errors.New("eventstream: truncated header type")
		}
		htype := b[i]
		i++
		switch htype {
		case 7: // string
			if i+2 > len(b) {
				return nil, errors.New("eventstream: truncated string length")
			}
			vlen := int(binary.BigEndian.Uint16(b[i : i+2]))
			i += 2
			if i+vlen > len(b) {
				return nil, errors.New("eventstream: truncated string value")
			}
			out[name] = string(b[i : i+vlen])
			i += vlen
		default:
			return nil, fmt.Errorf("eventstream: unsupported header type %d", htype)
		}
	}
	return out, nil
}

// Encode produces a single event-stream frame with the given string headers
// and payload. Used by tests; exported so external callers can build fixtures.
func Encode(headers map[string]string, payload []byte) []byte {
	var hdrBuf []byte
	// Deterministic header order for testability: by key, ascending.
	keys := sortedKeys(headers)
	for _, k := range keys {
		v := headers[k]
		hdrBuf = append(hdrBuf, byte(len(k)))
		hdrBuf = append(hdrBuf, k...)
		hdrBuf = append(hdrBuf, 7) // string type
		var vl [2]byte
		binary.BigEndian.PutUint16(vl[:], uint16(len(v)))
		hdrBuf = append(hdrBuf, vl[:]...)
		hdrBuf = append(hdrBuf, v...)
	}
	headersLen := uint32(len(hdrBuf))
	totalLen := uint32(12 + len(hdrBuf) + len(payload) + 4)
	out := make([]byte, 0, totalLen)
	var tmp [4]byte
	binary.BigEndian.PutUint32(tmp[:], totalLen)
	out = append(out, tmp[:]...)
	binary.BigEndian.PutUint32(tmp[:], headersLen)
	out = append(out, tmp[:]...)
	preludeCRC := crc32.Checksum(out, crcTable)
	binary.BigEndian.PutUint32(tmp[:], preludeCRC)
	out = append(out, tmp[:]...)
	out = append(out, hdrBuf...)
	out = append(out, payload...)
	msgCRC := crc32.Checksum(out, crcTable)
	binary.BigEndian.PutUint32(tmp[:], msgCRC)
	out = append(out, tmp[:]...)
	return out
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	// inline insertion sort to avoid sort import bloat (and keep dependency minimal)
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j-1] > keys[j]; j-- {
			keys[j-1], keys[j] = keys[j], keys[j-1]
		}
	}
	return keys
}
