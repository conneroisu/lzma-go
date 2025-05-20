package lzma

import (
	"fmt"
	"io"
)

type outWindow struct {
	w         io.Writer
	buf       []byte
	winSize   uint32
	pos       uint32
	streamPos uint32
}

func newOutWindow(w io.Writer, windowSize uint32) *outWindow {
	return &outWindow{
		w:         w,
		buf:       make([]byte, windowSize),
		winSize:   windowSize,
		pos:       0,
		streamPos: 0,
	}
}

func (ow *outWindow) flush() error {
	size := ow.pos - ow.streamPos
	if size == 0 {
		return nil
	}
	n, err := ow.w.Write(ow.buf[ow.streamPos : ow.streamPos+size])
	if err != nil {
		return err
	}
	if uint32(n) != size {
		return &NWriteError{
			msg: fmt.Sprintf("Write %d bytes, expected %d", n, size),
		}
	}
	if ow.pos >= ow.winSize {
		ow.pos = 0
	}
	ow.streamPos = ow.pos

	return nil
}

func (ow *outWindow) copyBlock(distance, length uint32) {
	pos := ow.pos - distance - 1
	if pos >= ow.winSize {
		pos += ow.winSize
	}
	for ; length != 0; length-- {
		if pos >= ow.winSize {
			pos = 0
		}
		ow.buf[ow.pos] = ow.buf[pos]
		ow.pos++
		pos++
		if ow.pos >= ow.winSize {
			ow.flush()
		}
	}
}

func (ow *outWindow) putByte(b byte) {
	ow.buf[ow.pos] = b
	ow.pos++
	if ow.pos >= ow.winSize {
		ow.flush()
	}
}

func (ow *outWindow) getByte(distance uint32) byte {
	pos := ow.pos - distance - 1
	if pos >= ow.winSize {
		pos += ow.winSize
	}

	return ow.buf[pos]
}

type inWindow struct {
	r              io.Reader
	buf            []byte
	posLimit       uint32
	lastSafePos    uint32
	bufOffset      uint32
	blockSize      uint32
	pos            uint32
	keepSizeBefore uint32
	keepSizeAfter  uint32
	streamPos      uint32
	streamEnd      bool
}

func newInWindow(
	r io.Reader,
	keepSizeBefore, keepSizeAfter, keepSizeReserv uint32,
) (*inWindow, error) {
	blockSize := keepSizeBefore + keepSizeAfter + keepSizeReserv
	iw := &inWindow{
		r:              r,
		buf:            make([]byte, blockSize),
		lastSafePos:    blockSize - keepSizeAfter,
		bufOffset:      0,
		blockSize:      blockSize,
		pos:            0,
		keepSizeBefore: keepSizeBefore,
		keepSizeAfter:  keepSizeAfter,
		streamPos:      0,
		streamEnd:      false,
	}
	err := iw.readBlock()

	return iw, err
}

func (iw *inWindow) moveBlock() {
	offset := iw.bufOffset + iw.pos - iw.keepSizeBefore
	if offset > 0 {
		offset--
	}
	numBytes := iw.bufOffset + iw.streamPos - offset
	var i uint32
	for ; i < numBytes; i++ {
		iw.buf[i] = iw.buf[offset+i]
	}
	iw.bufOffset -= offset
}

func (iw *inWindow) readBlock() error {
	if iw.streamEnd {
		return nil
	}
	for {
		if iw.blockSize-iw.bufOffset-iw.streamPos == 0 {
			return nil
		}
		n, err := iw.r.Read(iw.buf[iw.bufOffset+iw.streamPos : iw.blockSize])
		if err != nil && err != io.EOF {
			return err
		}
		if n == 0 && err == io.EOF {
			iw.posLimit = iw.streamPos
			ptr := iw.bufOffset + iw.posLimit
			if ptr > iw.lastSafePos {
				iw.posLimit = iw.lastSafePos - iw.bufOffset
			}
			iw.streamEnd = true

			return nil
		}
		iw.streamPos += uint32(n)
		if iw.streamPos >= iw.pos+iw.keepSizeAfter {
			iw.posLimit = iw.streamPos - iw.keepSizeAfter
		}
	}
}

func (iw *inWindow) movePos() error {
	iw.pos++
	if iw.pos > iw.posLimit {
		ptr := iw.bufOffset + iw.pos
		if ptr > iw.lastSafePos {
			iw.moveBlock()
		}
		err := iw.readBlock()
		if err != nil {
			return err
		}
	}

	return nil
}

func (iw *inWindow) getIndexByte(index int32) byte {
	return iw.buf[int32(iw.bufOffset+iw.pos)+index]
}

func (iw *inWindow) getMatchLen(
	index int32,
	distance, limit uint32,
) uint32 {
	var res uint32
	uIndex := uint32(index)
	if iw.streamEnd {
		if iw.pos+uIndex+limit > iw.streamPos {
			limit = iw.streamPos - (iw.pos + uIndex)
		}
	}
	distance++
	pby := iw.bufOffset + iw.pos + uIndex

	for ; res < limit && iw.buf[pby+res] == iw.buf[pby+res-distance]; res++ {
		continue
	}

	return res
}

func (iw *inWindow) getNumAvailableBytes() uint32 {
	return iw.streamPos - iw.pos
}

func (iw *inWindow) reduceOffsets(subValue uint32) {
	iw.bufOffset += subValue
	iw.posLimit -= subValue
	iw.pos -= subValue
	iw.streamPos -= subValue
}
