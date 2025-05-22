package lzma_test

import (
	"bytes"
	"errors"
	"io"
	"log"
	"testing"

	"github.com/conneroisu/lzma-go"
)

func TestDecoder(t *testing.T) {
	b := new(bytes.Buffer)
	for _, tt := range lzmaTests {
		t.Run("decoder_test - "+tt.desc, func(t *testing.T) {
			in := bytes.NewBuffer(tt.lzma)
			r := lzma.NewReader(in)
			defer r.Close()
			b.Reset()
			n, err := io.Copy(b, r)
			if errors.Is(err, tt.err) {
				return
			}
			if tt.err != nil {
				if errors.As(err, &lzma.HeaderError{}) {
					return
				}
			}
			if err == nil { // if err != nil, there is little chance that data is decoded correctly, if at all
				s := b.String()
				if s != tt.raw {
					t.Errorf("%s: got %d-byte %q, want %d-byte %q", tt.desc, n, s, len(tt.raw), tt.raw)
				}
			}
		})
	}
}

func BenchmarkDecoder(b *testing.B) {
	buf := new(bytes.Buffer)
	for b.Loop() {
		buf.Reset()
		in := bytes.NewBuffer(bench.lzma)
		b.StartTimer()
		// timer starts before this contructor because variable "in" already
		// contains data, so the decoding start rigth away
		r := lzma.NewReader(in)
		n, err := io.Copy(buf, r)
		b.StopTimer()
		if err != nil {
			log.Fatalf("%v", err)
		}
		b.SetBytes(n)
		r.Close()
	}
	if bytes.Equal(buf.Bytes(), bench.raw) == false { // check only after last iteration
		log.Fatalf("%s: got %d-byte %q, want %d-byte %q", bench.descr, len(buf.Bytes()), buf.String(), len(bench.raw), bench.raw)
	}
}
