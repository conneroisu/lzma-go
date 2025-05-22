package lzma

import (
	"fmt"
	"io"
)

const (
	inBufSize       = 1 << 16
	outBufSize      = 1 << 16
	propSize        = 5
	headerSize      = propSize + 8
	maxReqInputSize = 20

	kNumRepDistances                = 4
	kNumStates                      = 12
	kNumPosSlotBits                 = 6
	kDicLogSizeMin                  = 0
	kNumLenToPosStatesBits          = 2
	kNumLenToPosStates              = 1 << kNumLenToPosStatesBits
	kMatchMinLen                    = 2
	kNumAlignBits                   = 4
	kAlignTableSize                 = 1 << kNumAlignBits
	kAlignMask                      = kAlignTableSize - 1
	kStartPosModelIndex             = 4
	kEndPosModelIndex               = 14
	kNumPosModels                   = kEndPosModelIndex - kStartPosModelIndex
	kNumFullDistances               = 1 << (kEndPosModelIndex / 2)
	kNumLitPosStatesBitsEncodingMax = 4
	kNumLitContextBitsMax           = 8
	kNumPosStatesBitsMax            = 4
	kNumPosStatesMax                = 1 << kNumPosStatesBitsMax
	kNumLowLenBits                  = 3
	kNumMidLenBits                  = 3
	kNumHighLenBits                 = 8
	kNumLowLenSymbols               = 1 << kNumLowLenBits
	kNumMidLenSymbols               = 1 << kNumMidLenBits
	kNumLenSymbols                  = kNumLowLenSymbols + kNumMidLenSymbols + (1 << kNumHighLenBits)
	kMatchMaxLen                    = kMatchMinLen + kNumLenSymbols - 1
)

// NewReader returns a new [io.ReadCloser] that can be used to read the
// uncompressed version of `r`
//
// It is the caller's responsibility to call Close on the [io.ReadCloser] when
// finished reading.
func NewReader(r io.Reader) io.ReadCloser {
	var z decoder
	pr, pw := io.Pipe()
	go func() {
		err := z.decoder(r, pw)
		pw.CloseWithError(err)
	}()

	return pr
}

type decoder struct {
	// i/o
	rd     *rangeDecoder // reader
	outWin *outWindow    // writer

	// lzma header
	prop       *props
	unpackSize int64

	// hz
	matchDecoders    []uint16
	repDecoders      []uint16
	repG0Decoders    []uint16
	repG1Decoders    []uint16
	repG2Decoders    []uint16
	rep0LongDecoders []uint16
	posSlotCoders    []*rangeBitTreeCoder
	posDecoders      []uint16
	posAlignCoder    *rangeBitTreeCoder
	lenCoder         *lenCoder
	repLenCoder      *lenCoder
	litCoder         *litCoder
	dictSizeCheck    uint32
	posStateMask     uint32
}

// lzma properties (lc,lp, pb in encoded form).
type props struct {
	litContextBits, // lc
	litPosStateBits, // lp
	posStateBits uint8 // pb
	dictSize uint32
}

func (p *props) decodeProps(buf []byte) error {
	d := buf[0]
	if d > (9 * 5 * 5) {
		return &HeaderError{
			msg: fmt.Sprintf(
				"invalid lc, lp, pb identifier in header: %d",
				d,
			),
		}
	}
	p.litContextBits = d % 9
	d /= 9
	p.posStateBits = d / 5
	p.litPosStateBits = d % 5
	if p.litContextBits > kNumLitContextBitsMax ||
		p.litPosStateBits > 4 ||
		p.posStateBits > kNumPosStatesBitsMax {
		return &HeaderError{
			msg: fmt.Sprintf(
				"invalid lc, lp, pb value in header: %d",
				d,
			),
		}
	}
	for i := range 4 {
		p.dictSize += uint32(buf[i+1]) << uint32(i*8)
	}

	return nil
}

func (z *decoder) doDecode() error {
	var state, repr0, repr1, repr2, repr3 uint32
	var nowPos uint64
	var prevByte byte

	for z.unpackSize < 0 || int64(nowPos) < z.unpackSize {
		posState := uint32(nowPos) & z.posStateMask

		decoded, err := z.rd.decodeBit(
			z.matchDecoders,
			state<<kNumPosStatesBitsMax+posState,
		)
		if err != nil {
			return err
		}
		if decoded == 0 {
			lsc := z.litCoder.getSubCoder(uint32(nowPos), prevByte)
			if stateIsCharState(state) {
				prevByte, err = lsc.decodeNormal(z.rd)
				if err != nil {
					return err
				}
			} else {
				prevByte, err = lsc.decodeWithMatchByte(
					z.rd,
					z.outWin.getByte(repr0),
				)
				if err != nil {
					return err
				}
			}
			z.outWin.putByte(prevByte)
			state = stateUpdateChar(state)
			nowPos++

			continue
		}

		var length uint32
		decoded, err = z.rd.decodeBit(z.repDecoders, state)
		if err != nil {
			return err
		}
		if decoded != 1 {
			repr3, repr2, repr1 = repr2, repr1, repr0
			var posSlot uint32
			decoded, err = z.lenCoder.decode(z.rd, posState)
			if err != nil {
				return err
			}
			length = decoded + kMatchMinLen
			state = stateUpdateMatch(state)
			posSlot, err = z.posSlotCoders[getLenToPosState(length)].decode(z.rd)
			if err != nil {
				return err
			}
			if posSlot >= kStartPosModelIndex {
				numDirectBits := posSlot>>1 - 1
				repr0 = (2 | posSlot&1) << numDirectBits
				if posSlot < kEndPosModelIndex {
					decoded, err = reverseDecodeIndex(
						z.rd,
						z.posDecoders,
						repr0-posSlot-1,
						numDirectBits,
					)
					if err != nil {
						return err
					}
					repr0 += decoded
				} else {
					decoded, err = z.rd.decodeDirectBits(
						numDirectBits - kNumAlignBits,
					)
					if err != nil {
						return err
					}
					repr0 += decoded << kNumAlignBits
					decoded, err = z.posAlignCoder.reverseDecode(z.rd)
					if err != nil {
						return err
					}
					repr0 += decoded
					if int32(repr0) < 0 {
						if repr0 == 0xFFFFFFFF {
							break
						}

						return &StreamError{
							msg: fmt.Sprintf(
								"invalid rep0 value: %d",
								repr0,
							),
						}
					}
				}
			} else {
				repr0 = posSlot
			}
		} else {
			length = 0
			decoded, err = z.rd.decodeBit(z.repG0Decoders, state)
			if err != nil {
				return err
			}
			if decoded == 0 {
				decoded, err = z.rd.decodeBit(
					z.rep0LongDecoders,
					state<<kNumPosStatesBitsMax+posState,
				)
				if err != nil {
					return err
				}
				if decoded == 0 {
					state = stateUpdateShortRep(state)
					length = 1
				}
			} else {
				var distance uint32
				decoded, err = z.rd.decodeBit(
					z.repG1Decoders,
					state,
				)
				if err != nil {
					return err
				}
				if decoded == 0 {
					distance = repr1
				} else {
					decoded, err = z.rd.decodeBit(
						z.repG2Decoders,
						state,
					)
					if err != nil {
						return err
					}
					if decoded == 0 {
						distance = repr2
					} else {
						distance, repr3 = repr3, repr2
					}
					repr2 = repr1
				}
				repr1, repr0 = repr0, distance
			}
			if length == 0 {
				decoded, err = z.repLenCoder.decode(z.rd, posState)
				if err != nil {
					return err
				}
				length = decoded + kMatchMinLen
				state = stateUpdateRep(state)
			}
		}
		if uint64(repr0) >= nowPos {
			return &StreamError{
				msg: fmt.Sprintf(
					"invalid rep0 value: %d",
					repr0,
				),
			}
		}
		if repr0 >= z.dictSizeCheck {
			return &StreamError{
				msg: fmt.Sprintf(
					"invalid rep0 value (is greater than or equal to dictSizeCheck): %d",
					repr0,
				),
			}
		}
		z.outWin.copyBlock(repr0, length)
		nowPos += uint64(length)
		prevByte = z.outWin.getByte(0)
	}
	z.outWin.flush()

	return nil
}

func (z *decoder) decoder(r io.Reader, w io.Writer) (err error) {
	defer handlePanics(&err)

	// read 13 bytes (lzma header)
	header := make([]byte, headerSize)
	_, err = io.ReadFull(r, header)
	if err != nil {
		return
	}
	z.prop = &props{}
	err = z.prop.decodeProps(header)
	if err != nil {
		return err
	}

	z.unpackSize = 0
	for i := range 8 {
		b := header[propSize+i]
		z.unpackSize |= int64(b) << uint64(8*i)
	}

	// do not move before r.Read(header)
	z.rd, err = newRangeDecoder(r)
	if err != nil {
		return err
	}

	z.dictSizeCheck = max(z.prop.dictSize, 1)
	z.outWin = newOutWindow(w, max(z.dictSizeCheck, 1<<12))

	z.litCoder = newLitCoder(uint32(z.prop.litPosStateBits), uint32(z.prop.litContextBits))
	z.lenCoder = newLenCoder(uint32(1 << z.prop.posStateBits))
	z.repLenCoder = newLenCoder(uint32(1 << z.prop.posStateBits))
	z.posStateMask = uint32(1<<z.prop.posStateBits - 1)
	z.matchDecoders = initBitModels(kNumStates << kNumPosStatesBitsMax)
	z.repDecoders = initBitModels(kNumStates)
	z.repG0Decoders = initBitModels(kNumStates)
	z.repG1Decoders = initBitModels(kNumStates)
	z.repG2Decoders = initBitModels(kNumStates)
	z.rep0LongDecoders = initBitModels(kNumStates << kNumPosStatesBitsMax)
	z.posDecoders = initBitModels(kNumFullDistances - kEndPosModelIndex)
	z.posSlotCoders = make([]*rangeBitTreeCoder, kNumLenToPosStates)
	for i := range kNumLenToPosStates {
		z.posSlotCoders[i] = newRangeBitTreeCoder(kNumPosSlotBits)
	}
	z.posAlignCoder = newRangeBitTreeCoder(kNumAlignBits)

	err = z.doDecode()
	if err != nil {
		return err
	}

	return
}

func stateUpdateChar(index uint32) uint32 {
	if index < 4 {
		return 0
	}
	if index < 10 {
		return index - 3
	}

	return index - 6
}

func stateUpdateMatch(index uint32) uint32 {
	if index < 7 {
		return 7
	}

	return 10
}

func stateUpdateRep(index uint32) uint32 {
	if index < 7 {
		return 8
	}

	return 11
}

func stateUpdateShortRep(index uint32) uint32 {
	if index < 7 {
		return 9
	}

	return 11
}

func stateIsCharState(index uint32) bool { return index < 7 }

func getLenToPosState(length uint32) uint32 {
	length -= kMatchMinLen
	if length < kNumLenToPosStates {
		return length
	}

	return kNumLenToPosStates - 1
}
