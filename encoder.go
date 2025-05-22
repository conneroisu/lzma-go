package lzma

import (
	"fmt"
	"io"
	"strconv"
	"strings"
)

const (
	// BestSpeed is the fastest compression level.
	BestSpeed = 1
	// BestCompression is the compression level that gives the best compression ratio.
	BestCompression = 9
	// DefaultCompression is the default compression level.
	DefaultCompression = 5
)

// NewWriterSizeLevel writes to the given Writer the compressed version of
// data written to the returned [io.WriteCloser]. It is the caller's responsibility
// to call Close on the [io.WriteCloser] when done.
//
// Parameter size is the actual size of
// uncompressed data that's going to be written to [io.WriteCloser]. If size is
// unknown, use -1 instead.
//
// Parameter level is any integer value between [lzma.BestSpeed] and
// [lzma.BestCompression].
//
// Arguments size and level (the lzma header) are written to the writer before
// any compressed data.
//
// If size is -1, last bytes are encoded in a different way to mark the end of
// the stream. The size of the compressed data will increase by 5 or 6 bytes.
//
// The reason for which size is an argument is that, unlike gzip which appends
// the size and the checksum at the end of the stream, lzma stores the size
// before any compressed data. Thus, lzma can compute the size while reading
// data from pipe.
func NewWriterSizeLevel(w io.Writer, size int64, level int) (io.WriteCloser, error) {
	var z encoder
	pr, pw := newSyncPipe()
	go func() {
		err := z.encoder(pr, w, size, level)
		err = pr.CloseWithError(err)
		if err != nil {
			throw(fmt.Errorf("lzma.NewWriterSizeLevel failed to close: %w", err))
		}
	}()

	return pw, nil
}

// NewWriterLevel creates a new Writer that compresses data to the given Writer
// using the given level.
//
// Level is any integer value between [lzma.BestSpeed] and
// [lzma.BestCompression].
//
// Same as lzma.NewWriterSizeLevel(w, -1, level).
func NewWriterLevel(w io.Writer, level int) (io.WriteCloser, error) {
	return NewWriterSizeLevel(w, -1, level)
}

// NewWriterSize creates a new Writer that compresses data to the given Writer
// using the given size as the uncompressed data size.
//
// If size is unknown, use
// -1 instead.
//
// Level is any integer value between [lzma.BestSpeed] and
// [lzma.BestCompression].
//
// Parameter size and the size, [lzma.DefaultCompression], (the lzma header) are written to the passed in writer before
// any compressed data.
//
// If size is -1, last bytes are encoded in a different way to mark the end of
// the stream. The size of the compressed data will increase by 5 or 6 bytes.
//
// Same as NewWriterSizeLevel(w, size, lzma.DefaultCompression).
func NewWriterSize(w io.Writer, size int64) (io.WriteCloser, error) {
	return NewWriterSizeLevel(w, size, DefaultCompression)
}

// NewWriter creates a new Writer that compresses data to the given Writer
// using the default compression level.
//
// Same as NewWriterSizeLevel(w, -1, DefaultCompression).
func NewWriter(w io.Writer) (io.WriteCloser, error) {
	return NewWriterSizeLevel(w, -1, DefaultCompression)
}

// levels is intended to be constant, but there is no way to enforce this constraint.
var levels = []compressionLevel{
	{},                        // 0
	{16, 64, 3, 0, 2, "bt4"},  // 1
	{18, 64, 3, 0, 2, "bt4"},  // 2
	{20, 64, 3, 0, 2, "bt4"},  // 3
	{22, 128, 3, 0, 2, "bt4"}, // 4
	{23, 128, 3, 0, 2, "bt4"}, // 5
	{24, 128, 3, 0, 2, "bt4"}, // 6
	{25, 256, 3, 0, 2, "bt4"}, // 7
	{26, 256, 3, 0, 2, "bt4"}, // 8
	{27, 256, 3, 0, 2, "bt4"}, // 9
}

var gFastPos = make([]byte, 1<<11)

const (
	eMatchFinderTypeBT2  = 0
	eMatchFinderTypeBT4  = 1
	kInfinityPrice       = 0x0FFFFFFF
	kDefaultDicLogSize   = 22
	kNumFastBytesDefault = 0x20
	kNumLenSpecSymbols   = kNumLowLenSymbols + kNumMidLenSymbols
	kNumOpts             = 1 << 12
)

type compressionLevel struct {
	dictSize        uint32 // d, 1 << dictSize
	fastBytes       uint32 // fb
	litContextBits  uint32 // lc
	litPosStateBits uint32 // lp (not used)
	posStateBits    uint32 // pb
	matchFinder     string // mf
}

// should be called in the encoder's contructor.
func initGFastPos() {
	kFastSlots := 22
	c := 2
	gFastPos[0] = 0
	gFastPos[1] = 1
	for slotFast := 2; slotFast < kFastSlots; slotFast++ {
		k := 1 << uint(slotFast>>1-1)
		for j := 0; j < k; j, c = j+1, c+1 {
			gFastPos[c] = byte(slotFast)
		}
	}
}

func getPosSlot(pos uint32) uint32 {
	if pos < 1<<11 {
		return uint32(gFastPos[pos])
	}
	if pos < 1<<21 {
		return uint32(gFastPos[pos>>10] + 20)
	}

	return uint32(gFastPos[pos>>20] + 40)
}

func getPosSlot2(pos uint32) uint32 {
	if pos < 1<<17 {
		return uint32(gFastPos[pos>>6] + 12)
	}
	if pos < 1<<27 {
		return uint32(gFastPos[pos>>16] + 32)
	}

	return uint32(gFastPos[pos>>26] + 52)
}

type optimal struct {
	state,
	posPrev2,
	backPrev2,
	price,
	posPrev,
	backPrev,
	backs0,
	backs1,
	backs2,
	backs3 uint32

	prev1IsChar,
	prev2 bool
}

func (o *optimal) makeAsChar() {
	o.backPrev = 0xFFFFFFFF
	o.prev1IsChar = false
}

func (o *optimal) makeAsShortRep() {
	o.backPrev = 0
	o.prev1IsChar = false
}

func (o *optimal) isShortRep() bool {
	return o.backPrev == 0
}

type encoder struct {
	// i/o, range encoder and match finder
	re *rangeEncoder // w
	mf *binTree      // r

	cl           *compressionLevel
	size         int64
	writeEndMark bool // eos

	optimum []*optimal

	isMatch    []uint16
	isRep      []uint16
	isRepG0    []uint16
	isRepG1    []uint16
	isRepG2    []uint16
	isRep0Long []uint16

	posSlotCoders []*rangeBitTreeCoder

	posCoders     []uint16
	posAlignCoder *rangeBitTreeCoder

	lenCoder         *lenPriceTableCoder
	repMatchLenCoder *lenPriceTableCoder

	litCoder *litCoder

	matchDistances []uint32

	longestMatchLen uint32
	distancePairs   uint32

	additionalOffset uint32

	optimumEndIndex     uint32
	optimumCurrentIndex uint32

	longestMatchFound bool

	posSlotPrices   []uint32
	distancesPrices []uint32
	alignPrices     []uint32
	alignPriceCount uint32

	distTableSize uint32

	posStateMask uint32

	nowPos   int64
	finished bool

	matchFinderType uint32

	state           uint32
	prevByte        byte
	repDistances    []uint32
	matchPriceCount uint32

	reps    []uint32
	repLens []uint32
	backRes uint32
}

func (z *encoder) readMatchDistances() (uint32, error) {
	var lenRes uint32
	var err error
	z.distancePairs, err = z.mf.getMatches(z.matchDistances)
	if err != nil {
		return 0, err
	}
	if z.distancePairs > 0 {
		lenRes = z.matchDistances[z.distancePairs-2]
		if lenRes == z.cl.fastBytes {
			lenRes += z.mf.iw.getMatchLen(int32(lenRes)-1, z.matchDistances[z.distancePairs-1], kMatchMaxLen-lenRes)
		}
	}
	z.additionalOffset++

	return lenRes, nil
}

func (z *encoder) movePos(num uint32) error {
	if num > 0 {
		z.additionalOffset += num

		return z.mf.skip(num)
	}

	return nil
}

func (z *encoder) getPureRepPrice(repIndex, state, posState uint32) (price uint32) {
	if repIndex == 0 {
		price = getPrice0(z.isRepG0[state])
		price += getPrice1(z.isRep0Long[state<<kNumPosStatesBitsMax+posState])
	} else {
		price = getPrice1(z.isRepG0[state])
		if repIndex == 1 {
			price += getPrice0(z.isRepG1[state])
		} else {
			price += getPrice1(z.isRepG1[state])
			price += getPrice(z.isRepG2[state], repIndex-2)
		}
	}

	return
}

func (z *encoder) getRepPrice(repIndex, length, state, posState uint32) (price uint32) {
	price = z.repMatchLenCoder.getPrice(length-kMatchMinLen, posState)
	price += z.getPureRepPrice(repIndex, state, posState)

	return
}

func (z *encoder) getPosLenPrice(pos, length, posState uint32) (price uint32) {
	lenToPosState := getLenToPosState(length)
	if pos < kNumFullDistances {
		price = z.distancesPrices[lenToPosState*kNumFullDistances+pos]
	} else {
		price = z.posSlotPrices[lenToPosState<<kNumPosSlotBits+getPosSlot2(pos)] + z.alignPrices[pos&kAlignMask]
	}
	price += z.lenCoder.getPrice(length-kMatchMinLen, posState)

	return
}

func (z *encoder) getRepLen1Price(state, posState uint32) uint32 {
	return getPrice0(z.isRepG0[state]) + getPrice0(z.isRep0Long[state<<kNumPosStatesBitsMax+posState])
}

func (z *encoder) backward(cur uint32) uint32 {
	z.optimumEndIndex = cur
	posMem := z.optimum[cur].posPrev
	backMem := z.optimum[cur].backPrev
	tmp := uint32(1) // execute the loop at least once (do-while)
	for ; tmp > 0; tmp = cur {
		if z.optimum[cur].prev1IsChar {
			z.optimum[posMem].makeAsChar()
			z.optimum[posMem].posPrev = posMem - 1
			if z.optimum[cur].prev2 {
				z.optimum[posMem-1].prev1IsChar = false
				z.optimum[posMem-1].posPrev = z.optimum[cur].posPrev2
				z.optimum[posMem-1].backPrev = z.optimum[cur].backPrev2
			}
		}
		posPrev := posMem
		backCur := backMem
		backMem = z.optimum[posPrev].backPrev
		posMem = z.optimum[posPrev].posPrev
		z.optimum[posPrev].backPrev = backCur
		z.optimum[posPrev].posPrev = cur
		cur = posPrev
	}
	z.backRes = z.optimum[0].backPrev
	z.optimumCurrentIndex = z.optimum[0].posPrev

	return z.optimumCurrentIndex
}

func (z *encoder) getOptimum(position uint32) (uint32, error) {
	if z.optimumEndIndex != z.optimumCurrentIndex {
		lenRes := z.optimum[z.optimumCurrentIndex].posPrev - z.optimumCurrentIndex
		z.backRes = z.optimum[z.optimumCurrentIndex].backPrev
		z.optimumCurrentIndex = z.optimum[z.optimumCurrentIndex].posPrev

		return lenRes, nil
	}

	z.optimumEndIndex = 0
	z.optimumCurrentIndex = 0
	var res uint32
	var lenMain uint32
	var distancePairs uint32
	var err error
	if !z.longestMatchFound {
		lenMain, err = z.readMatchDistances()
		if err != nil {
			return 0, err
		}
	} else {
		lenMain = z.longestMatchLen
		z.longestMatchFound = false
	}
	distancePairs = z.distancePairs
	availableBytes := z.mf.iw.getNumAvailableBytes() + 1
	if availableBytes < 2 {
		z.backRes = 0xFFFFFFFF

		return 1, nil
	}

	var i, repMaxIndex uint32
	for ; i < kNumRepDistances; i++ {
		z.reps[i] = z.repDistances[i]
		z.repLens[i] = z.mf.iw.getMatchLen(0-1, z.reps[i], kMatchMaxLen)
		if z.repLens[i] > z.repLens[repMaxIndex] {
			repMaxIndex = i
		}
	}
	if z.repLens[repMaxIndex] >= z.cl.fastBytes {
		z.backRes = repMaxIndex
		lenRes := z.repLens[repMaxIndex]
		err = z.movePos(lenRes - 1)

		return lenRes, err
	}

	if lenMain >= z.cl.fastBytes {
		z.backRes = z.matchDistances[distancePairs-1] + kNumRepDistances
		err = z.movePos(lenMain - 1)

		return lenMain, err
	}

	curByte := z.mf.iw.getIndexByte(0 - 1)
	matchByte := z.mf.iw.getIndexByte(0 - int32(z.repDistances[0]) - 1 - 1)
	if lenMain < 2 && curByte != matchByte && z.repLens[repMaxIndex] < 2 {
		z.backRes = 0xFFFFFFFF

		return 1, nil
	}

	z.optimum[0].state = z.state
	posState := position & z.posStateMask
	z.optimum[1].price = getPrice0(z.isMatch[z.state<<kNumPosStatesBitsMax+posState]) +
		z.litCoder.getSubCoder(position, z.prevByte).getPrice(!stateIsCharState(z.state), matchByte, curByte)
	z.optimum[1].makeAsChar()

	matchPrice := getPrice1(z.isMatch[z.state<<kNumPosStatesBitsMax+posState])
	repMatchPrice := matchPrice + getPrice1(z.isRep[z.state])
	if matchByte == curByte {
		shortRepPrice := repMatchPrice + z.getRepLen1Price(z.state, posState)
		if shortRepPrice < z.optimum[1].price {
			z.optimum[1].price = shortRepPrice
			z.optimum[1].makeAsShortRep()
		}
	}

	lenEnd := max(lenMain, z.repLens[repMaxIndex])
	if lenEnd < 2 {
		z.backRes = z.optimum[1].backPrev

		return 1, nil
	}

	z.optimum[1].posPrev = 0
	z.optimum[0].backs0 = z.reps[0]
	z.optimum[0].backs1 = z.reps[1]
	z.optimum[0].backs2 = z.reps[2]
	z.optimum[0].backs3 = z.reps[3]
	length := lenEnd
DoWhile1:
	z.optimum[length].price = kInfinityPrice
	if length--; length >= 2 {
		goto DoWhile1
	}

	i = 0
	for ; i < kNumRepDistances; i++ {
		repLen := z.repLens[i]
		if repLen < 2 {
			continue
		}
		price := repMatchPrice + z.getPureRepPrice(i, z.state, posState)
	DoWhile2:
		curAndLenPrice := price + z.repMatchLenCoder.getPrice(repLen-2, posState)
		optimum := z.optimum[repLen]
		if curAndLenPrice < optimum.price {
			optimum.price = curAndLenPrice
			optimum.posPrev = 0
			optimum.backPrev = i
			optimum.prev1IsChar = false
		}
		if repLen--; repLen >= 2 {
			goto DoWhile2
		}
	}

	normalMatchPrice := matchPrice + getPrice0(z.isRep[z.state])
	length = 2
	if z.repLens[0] >= 2 {
		length = z.repLens[0] + 1
	}
	if length <= lenMain {
		var offs uint32
		for length > z.matchDistances[offs] {
			offs += 2
		}
		for ; ; length++ {
			distance := z.matchDistances[offs+1]
			curAndLenPrice := normalMatchPrice + z.getPosLenPrice(distance, length, posState)
			optimum := z.optimum[length]
			if curAndLenPrice < optimum.price {
				optimum.price = curAndLenPrice
				optimum.posPrev = 0
				optimum.backPrev = distance + kNumRepDistances
				optimum.prev1IsChar = false
			}
			if length == z.matchDistances[offs] {
				offs += 2
				if offs == distancePairs {
					break
				}
			}
		}
	}

	var cur uint32
	for {
		cur++
		if cur == lenEnd {
			res = z.backward(cur)

			return res, nil
		}

		newLen, err := z.readMatchDistances()
		if err != nil {
			return 0, err
		}
		distancePairs = z.distancePairs
		if newLen >= z.cl.fastBytes {
			z.longestMatchLen = newLen
			z.longestMatchFound = true
			res = z.backward(cur)

			return res, nil
		}

		position++
		posPrev := z.optimum[cur].posPrev
		var state uint32
		if z.optimum[cur].prev1IsChar {
			posPrev--
			if z.optimum[cur].prev2 {
				state = z.optimum[z.optimum[cur].posPrev2].state
				if z.optimum[cur].backPrev2 < kNumRepDistances {
					state = stateUpdateRep(state)
				} else {
					state = stateUpdateMatch(state)
				}
			} else {
				state = z.optimum[posPrev].state
			}
			state = stateUpdateChar(state)
		} else {
			state = z.optimum[posPrev].state
		}
		if posPrev == cur-1 {
			if z.optimum[cur].isShortRep() {
				state = stateUpdateShortRep(state)
			} else {
				state = stateUpdateChar(state)
			}
		} else {
			var pos uint32
			if z.optimum[cur].prev1IsChar && z.optimum[cur].prev2 {
				posPrev = z.optimum[cur].posPrev2
				pos = z.optimum[cur].backPrev2
				state = stateUpdateRep(state)
			} else {
				pos = z.optimum[cur].backPrev
				if pos < kNumRepDistances {
					state = stateUpdateRep(state)
				} else {
					state = stateUpdateMatch(state)
				}
			}
			opt := z.optimum[posPrev]
			if pos < kNumRepDistances {
				switch pos {
				case 0:
					z.reps[0] = opt.backs0
					z.reps[1] = opt.backs1
					z.reps[2] = opt.backs2
					z.reps[3] = opt.backs3
				case 1:
					z.reps[0] = opt.backs1
					z.reps[1] = opt.backs0
					z.reps[2] = opt.backs2
					z.reps[3] = opt.backs3
				case 2:
					z.reps[0] = opt.backs2
					z.reps[1] = opt.backs0
					z.reps[2] = opt.backs1
					z.reps[3] = opt.backs3
				default:
					z.reps[0] = opt.backs3
					z.reps[1] = opt.backs0
					z.reps[2] = opt.backs1
					z.reps[3] = opt.backs2
				}
			} else {
				z.reps[0] = pos - kNumRepDistances
				z.reps[1] = opt.backs0
				z.reps[2] = opt.backs1
				z.reps[3] = opt.backs2
			}
		}
		z.optimum[cur].state = state
		z.optimum[cur].backs0 = z.reps[0]
		z.optimum[cur].backs1 = z.reps[1]
		z.optimum[cur].backs2 = z.reps[2]
		z.optimum[cur].backs3 = z.reps[3]
		curPrice := z.optimum[cur].price
		curByte = z.mf.iw.getIndexByte(0 - 1)
		matchByte = z.mf.iw.getIndexByte(0 - int32(z.reps[0]) - 1 - 1)
		posState = position & z.posStateMask
		curAnd1Price := curPrice + getPrice0(z.isMatch[state<<kNumPosStatesBitsMax+posState]) +
			z.litCoder.getSubCoder(position, z.mf.iw.getIndexByte(0-2)).getPrice(!stateIsCharState(state), matchByte, curByte)

		nextOptimum := z.optimum[cur+1]
		nextIsChar := false
		if curAnd1Price < nextOptimum.price {
			nextOptimum.price = curAnd1Price
			nextOptimum.posPrev = cur
			nextOptimum.makeAsChar()
			nextIsChar = true
		}

		matchPrice = curPrice + getPrice1(z.isMatch[state<<kNumPosStatesBitsMax+posState])
		repMatchPrice = matchPrice + getPrice1(z.isRep[state])
		if matchByte == curByte && (nextOptimum.posPrev >= cur || nextOptimum.backPrev != 0) {
			shortRepPrice := repMatchPrice + z.getRepLen1Price(state, posState)
			if shortRepPrice <= nextOptimum.price {
				nextOptimum.price = shortRepPrice
				nextOptimum.posPrev = cur
				nextOptimum.makeAsShortRep()
				nextIsChar = true
			}
		}

		availableBytesFull := z.mf.iw.getNumAvailableBytes() + 1
		availableBytesFull = min(kNumOpts-1-cur, availableBytesFull)
		availableBytes = availableBytesFull
		if availableBytes < 2 {
			continue
		}
		if availableBytes > z.cl.fastBytes {
			availableBytes = z.cl.fastBytes
		}
		if !nextIsChar && matchByte != curByte {
			t := min(availableBytesFull-1, z.cl.fastBytes)
			lenTest2 := z.mf.iw.getMatchLen(0, z.reps[0], t)
			if lenTest2 >= 2 {
				state2 := stateUpdateChar(state)
				posStateNext := (position + 1) & z.posStateMask
				nextRepMatchPrice := curAnd1Price + getPrice1(z.isMatch[state2<<kNumPosStatesBitsMax+posStateNext]) +
					getPrice1(z.isRep[state2])
				offset := cur + 1 + lenTest2
				for lenEnd < offset {
					lenEnd++
					z.optimum[lenEnd].price = kInfinityPrice
				}
				curAndLenPrice := nextRepMatchPrice + z.getRepPrice(0, lenTest2, state2, posStateNext)
				optimum := z.optimum[offset]
				if curAndLenPrice < optimum.price {
					optimum.price = curAndLenPrice
					optimum.posPrev = cur + 1
					optimum.backPrev = 0
					optimum.prev1IsChar = true
					optimum.prev2 = false
				}
			}
		}

		startLen := uint32(2)
		var repIndex uint32
		for ; repIndex < kNumRepDistances; repIndex++ {
			lenTest := z.mf.iw.getMatchLen(0-1, z.reps[repIndex], availableBytes)
			if lenTest < 2 {
				continue
			}
			lenTestTemp := lenTest
		DoWhile3:
			for lenEnd < cur+lenTest {
				lenEnd++
				z.optimum[lenEnd].price = kInfinityPrice
			}
			curAndLenPrice := repMatchPrice + z.getRepPrice(repIndex, lenTest, state, posState)
			optimum := z.optimum[cur+lenTest]
			if curAndLenPrice < optimum.price {
				optimum.price = curAndLenPrice
				optimum.posPrev = cur
				optimum.backPrev = repIndex
				optimum.prev1IsChar = false
			}
			if lenTest--; lenTest >= 2 {
				goto DoWhile3
			}

			lenTest = lenTestTemp
			if repIndex == 0 {
				startLen = lenTest + 1
			}

			if lenTest < availableBytesFull {
				t := min(availableBytesFull-1-lenTest, z.cl.fastBytes)
				lenTest2 := z.mf.iw.getMatchLen(int32(lenTest), z.reps[repIndex], t)
				if lenTest2 >= 2 {
					state2 := stateUpdateRep(state)
					posStateNext := (position + lenTest) & z.posStateMask
					curAndLenCharPrice := repMatchPrice + z.getRepPrice(repIndex, lenTest, state, posState) +
						getPrice0(z.isMatch[state2<<kNumPosStatesBitsMax+posStateNext]) +
						z.litCoder.getSubCoder(
							position+lenTest,
							z.mf.iw.getIndexByte(int32(lenTest)-1-1),
						).getPrice(
							true,
							z.mf.iw.getIndexByte(int32(lenTest)-1-(int32(z.reps[repIndex]+1))),
							z.mf.iw.getIndexByte(int32(lenTest)-1),
						)
					state2 = stateUpdateChar(state2)
					posStateNext = (position + lenTest + 1) & z.posStateMask
					nextMatchPrice := curAndLenCharPrice + getPrice1(z.isMatch[state2<<kNumPosStatesBitsMax+posStateNext])
					nextRepMatchPrice := nextMatchPrice + getPrice1(z.isRep[state2])

					offset := lenTest + 1 + lenTest2
					for lenEnd < cur+offset {
						lenEnd++
						z.optimum[lenEnd].price = kInfinityPrice
					}
					curAndLenPrice := nextRepMatchPrice + z.getRepPrice(0, lenTest2, state2, posStateNext)
					optimum := z.optimum[cur+offset]
					if curAndLenPrice < optimum.price {
						optimum.price = curAndLenPrice
						optimum.posPrev = cur + lenTest + 1
						optimum.backPrev = 0
						optimum.prev1IsChar = true
						optimum.prev2 = true
						optimum.posPrev2 = cur
						optimum.backPrev2 = repIndex
					}
				}
			}
		}

		if newLen > availableBytes {
			newLen = availableBytes
			distancePairs = 0
			for {
				if newLen > z.matchDistances[distancePairs] {
					distancePairs += 2
				}

				break
			}
			z.matchDistances[distancePairs] = newLen
			distancePairs += 2
		}
		if newLen < startLen {
			continue
		}
		var offs uint32
		normalMatchPrice = matchPrice + getPrice0(z.isRep[state])
		for lenEnd < cur+newLen {
			lenEnd++
			z.optimum[lenEnd].price = kInfinityPrice
		}
		for startLen > z.matchDistances[offs] {
			offs += 2
		}

		for lenTest := startLen; ; lenTest++ {
			curBack := z.matchDistances[offs+1]
			curAndLenPrice := normalMatchPrice + z.getPosLenPrice(curBack, lenTest, posState)
			optimum := z.optimum[cur+lenTest]
			if curAndLenPrice < optimum.price {
				optimum.price = curAndLenPrice
				optimum.posPrev = cur
				optimum.backPrev = curBack + kNumRepDistances
				optimum.prev1IsChar = false
			}
			if lenTest != z.matchDistances[offs] {
				continue
			}
			if lenTest < availableBytesFull {
				t := min(availableBytesFull-1-lenTest, z.cl.fastBytes)
				lenTest2 := z.mf.iw.getMatchLen(int32(lenTest), curBack, t)
				if lenTest2 >= 2 {
					state2 := stateUpdateMatch(state)
					posStateNext := (position + lenTest) & z.posStateMask
					curAndLenCharPrice := curAndLenPrice +
						getPrice0(z.isMatch[state2<<kNumPosStatesBitsMax+posStateNext]) +
						z.litCoder.getSubCoder(
							position+lenTest,
							z.mf.iw.getIndexByte(int32(lenTest)-1-1),
						).getPrice(
							true, z.mf.iw.getIndexByte(int32(lenTest)-(int32(curBack)+1)-1),
							z.mf.iw.getIndexByte(int32(lenTest)-1))

					state2 = stateUpdateChar(state2)
					posStateNext = (position + lenTest + 1) & z.posStateMask
					nextMatchPrice := curAndLenCharPrice + getPrice1(z.isMatch[state2<<kNumPosStatesBitsMax+posStateNext])
					nextRepMatchPrice := nextMatchPrice + getPrice1(z.isRep[state2])
					offset := lenTest + 1 + lenTest2
					for lenEnd < cur+offset {
						lenEnd++
						z.optimum[lenEnd].price = kInfinityPrice
					}
					curAndLenPrice = nextRepMatchPrice + z.getRepPrice(
						0,
						lenTest2,
						state2,
						posStateNext,
					)
					optimum = z.optimum[cur+offset]
					if curAndLenPrice < optimum.price {
						optimum.price = curAndLenPrice
						optimum.posPrev = cur + lenTest + 1
						optimum.backPrev = 0
						optimum.prev1IsChar = true
						optimum.prev2 = true
						optimum.posPrev2 = cur
						optimum.backPrev2 = curBack + kNumRepDistances
					}
				}
			}
			offs += 2
			if offs == distancePairs {
				break
			}
		}
	}
}

var tempPrices = make([]uint32, kNumFullDistances)

func (z *encoder) fillDistancesPrices() {
	for i := uint32(kStartPosModelIndex); i < kNumFullDistances; i++ {
		posSlot := getPosSlot(i)
		footerBits := posSlot>>1 - 1
		baseVal := (2 | posSlot&1) << footerBits
		tempPrices[i] = reverseGetPriceIndex(z.posCoders, baseVal-posSlot-1, footerBits, i-baseVal)
	}
	var lenToPosState uint32
	for ; lenToPosState < kNumLenToPosStates; lenToPosState++ {
		var posSlot, i uint32
		st := lenToPosState << kNumPosSlotBits
		for posSlot = range z.distTableSize {
			z.posSlotPrices[st+posSlot] = z.posSlotCoders[lenToPosState].getPrice(posSlot)
		}
		for posSlot = kEndPosModelIndex; posSlot < z.distTableSize; posSlot++ {
			z.posSlotPrices[st+posSlot] += (posSlot>>1 - 1 - kNumAlignBits) << kNumBitPriceShiftBits
		}
		st2 := lenToPosState * kNumFullDistances
		for i = range kStartPosModelIndex {
			z.distancesPrices[st2+i] = z.posSlotPrices[st+i]
		}
		for ; i < kNumFullDistances; i++ {
			z.distancesPrices[st2+i] = z.posSlotPrices[st+getPosSlot(i)] + tempPrices[i]
		}
	}
	z.matchPriceCount = 0
}

func (z *encoder) fillAlignPrices() {
	var i uint32
	for ; i < kAlignTableSize; i++ {
		z.alignPrices[i] = z.posAlignCoder.reverseGetPrice(i)
	}
	z.alignPriceCount = 0
}

func (z *encoder) writeEndMarker(posState uint32) {
	if !z.writeEndMark {
		return
	}
	z.re.encode(z.isMatch, z.state<<kNumPosStatesBitsMax+posState, 1)
	z.re.encode(z.isRep, z.state, 0)
	z.state = stateUpdateMatch(z.state)
	length := kMatchMinLen
	z.lenCoder.encode(z.re, 0, posState) // 0 is length - kMatchMinLen
	posSlot := 1<<kNumPosSlotBits - 1
	lenToPosState := getLenToPosState(uint32(length))
	z.posSlotCoders[lenToPosState].encode(z.re, uint32(posSlot))
	footerBits := uint32(30)
	posReduced := uint32(1)<<footerBits - 1
	z.re.encodeDirectBits(posReduced>>kNumAlignBits, footerBits-kNumAlignBits)
	z.posAlignCoder.reverseEncode(z.re, posReduced&kAlignMask)
}

func (z *encoder) flush(nowPos uint32) {
	z.writeEndMarker(nowPos & z.posStateMask)
	z.re.flush()
}

func (z *encoder) codeOneBlock() {
	z.finished = true
	progressPosValuePrev := z.nowPos
	if z.nowPos == 0 {
		if z.mf.iw.getNumAvailableBytes() == 0 {
			z.flush(uint32(z.nowPos))

			return
		}
		_, err := z.readMatchDistances()
		if err != nil {
			return
		}
		z.re.encode(z.isMatch, z.state<<kNumPosStatesBitsMax+uint32(z.nowPos)&z.posStateMask, 0)
		z.state = stateUpdateChar(z.state)
		curByte := z.mf.iw.getIndexByte(0 - int32(z.additionalOffset))
		z.litCoder.getSubCoder(uint32(z.nowPos), z.prevByte).encode(z.re, curByte)
		z.prevByte = curByte
		z.additionalOffset--
		z.nowPos++
	}
	if z.mf.iw.getNumAvailableBytes() == 0 {
		z.flush(uint32(z.nowPos))

		return
	}
	for {
		length, err := z.getOptimum(uint32(z.nowPos))
		if err != nil {
			return
		}
		pos := z.backRes
		posState := uint32(z.nowPos) & z.posStateMask
		complexState := z.state<<kNumPosStatesBitsMax + posState

		if length == 1 && pos == 0xFFFFFFFF {
			z.re.encode(z.isMatch, complexState, 0)
			curByte := z.mf.iw.getIndexByte(0 - int32(z.additionalOffset))
			lsc := z.litCoder.getSubCoder(uint32(z.nowPos), z.prevByte)
			if !stateIsCharState(z.state) {
				matchByte := z.mf.iw.getIndexByte(0 - int32(z.repDistances[0]) - 1 - int32(z.additionalOffset))
				lsc.encodeMatched(z.re, matchByte, curByte)
			} else {
				lsc.encode(z.re, curByte)
			}
			z.prevByte = curByte
			z.state = stateUpdateChar(z.state)
		} else {
			z.re.encode(z.isMatch, complexState, 1)
			if pos < kNumRepDistances {
				z.re.encode(z.isRep, z.state, 1)
				if pos == 0 {
					z.re.encode(z.isRepG0, z.state, 0)
					if length == 1 {
						z.re.encode(z.isRep0Long, complexState, 0)
					} else {
						z.re.encode(z.isRep0Long, complexState, 1)
					}
				} else {
					z.re.encode(z.isRepG0, z.state, 1)
					if pos == 1 {
						z.re.encode(z.isRepG1, z.state, 0)
					} else {
						z.re.encode(z.isRepG1, z.state, 1)
						z.re.encode(z.isRepG2, z.state, pos-2)
					}
				}
				if length == 1 {
					z.state = stateUpdateShortRep(z.state)
				} else {
					z.repMatchLenCoder.encode(
						z.re,
						length-kMatchMinLen,
						posState,
					)
					z.state = stateUpdateRep(z.state)
				}
				distance := z.repDistances[pos]
				if pos != 0 {
					for i := pos; i >= 1; i-- {
						z.repDistances[i] = z.repDistances[i-1]
					}
					z.repDistances[0] = distance
				}
			} else {
				z.re.encode(z.isRep, z.state, 0)
				z.state = stateUpdateMatch(z.state)
				z.lenCoder.encode(z.re, length-kMatchMinLen, posState)
				pos -= kNumRepDistances
				posSlot := getPosSlot(pos)
				lenToPosState := getLenToPosState(length)
				z.posSlotCoders[lenToPosState].encode(z.re, posSlot)
				if posSlot >= kStartPosModelIndex {
					footerBits := posSlot>>1 - 1
					baseVal := (2 | posSlot&1) << footerBits
					posReduced := pos - baseVal
					if posSlot < kEndPosModelIndex {
						reverseEncodeIndex(
							z.re,
							z.posCoders,
							baseVal-posSlot-1,
							footerBits,
							posReduced,
						)
					} else {
						z.re.encodeDirectBits(
							posReduced>>kNumAlignBits,
							footerBits-kNumAlignBits,
						)
						z.posAlignCoder.reverseEncode(z.re, posReduced&kAlignMask)
						z.alignPriceCount++
					}
				}
				for i := kNumRepDistances - 1; i >= 1; i-- {
					z.repDistances[i] = z.repDistances[i-1]
				}
				z.repDistances[0] = pos
				z.matchPriceCount++
			}
			z.prevByte = z.mf.iw.getIndexByte(int32(length) - 1 - int32(z.additionalOffset))
		}
		z.additionalOffset -= length
		z.nowPos += int64(length)
		if z.additionalOffset == 0 {
			if z.matchPriceCount >= 1<<7 {
				z.fillDistancesPrices()
			}
			if z.alignPriceCount >= kAlignTableSize {
				z.fillAlignPrices()
			}
			if z.mf.iw.getNumAvailableBytes() == 0 {
				z.flush(uint32(z.nowPos))

				return
			}
			if z.nowPos-progressPosValuePrev >= 1<<12 {
				z.finished = false

				return
			}
		}
	}
}

func (z *encoder) doEncode() {
	for {
		z.codeOneBlock()
		if z.finished {
			break
		}
	}
}

func (z *encoder) encoder(
	r io.Reader,
	w io.Writer,
	size int64,
	level int,
) (err error) {
	defer handlePanics(&err)

	initProbPrices()
	initCrcTable()
	initGFastPos()

	if level < 1 || level > 9 {
		return &ArgumentValueError{"level out of range", level}
	}
	// do not assign &levels[level] directly to z.cl because dictSize is modified later
	// and the next run of this funcion with the same compression level will fail;
	// levels is intended to be const, but there is no way enforce this constraint.
	cl := levels[level]
	z.cl = &cl
	if z.cl.dictSize < 12 || cl.dictSize > 29 {
		return &ArgumentValueError{"dictionary size out of range", z.cl.dictSize}
	}
	if z.cl.fastBytes < 5 || cl.fastBytes > 273 {
		return &ArgumentValueError{"number of fast bytes out of range", z.cl.fastBytes}
	}
	if z.cl.litContextBits > 8 {
		return &ArgumentValueError{"number of literal context bits out of range", z.cl.litContextBits}
	}
	if z.cl.litPosStateBits > 4 {
		return &ArgumentValueError{"number of literal position bits out of range", z.cl.litPosStateBits}
	}
	if z.cl.posStateBits > 4 {
		return &ArgumentValueError{"number of position bits out of range", z.cl.posStateBits}
	}
	if z.cl.matchFinder != "bt2" && cl.matchFinder != "bt4" {
		return &ArgumentValueError{"unsuported match finder", z.cl.matchFinder}
	}
	z.distTableSize = z.cl.dictSize * 2
	z.cl.dictSize = 1 << z.cl.dictSize
	if size < -1 { // size can be equal to zero
		return &ArgumentValueError{"illegal size", size}
	}
	z.size = size
	z.writeEndMark = false
	if z.size == -1 {
		z.writeEndMark = true
	}

	header := make([]byte, headerSize)
	header[0] = byte((z.cl.posStateBits*5+z.cl.litPosStateBits)*9 + z.cl.litContextBits)
	var i uint32
	for ; i < 4; i++ {
		header[i+1] = byte(z.cl.dictSize >> (8 * i))
	}
	i = 0
	for ; i < 8; i++ {
		header[i+propSize] = byte(z.size >> (8 * i))
	}
	n, err := w.Write(header)
	if err != nil {
		return
	}
	if n != len(header) {
		return &NWriteError{
			msg: fmt.Sprintf("wrote %d bytes, want %d bytes", n, len(header)),
		}
	}

	// do not move before w.Write(header)
	z.re = newRangeEncoder(w)
	mft, err := strconv.ParseUint(strings.Split(z.cl.matchFinder, "")[2], 10, 64)
	if err != nil {
		return
	}
	z.matchFinderType = uint32(mft)
	numHashBytes := uint32(4)
	if z.matchFinderType == eMatchFinderTypeBT2 {
		numHashBytes = 2
	}
	z.mf, err = newBinTree(r, z.cl.dictSize, kNumOpts, z.cl.fastBytes, kMatchMaxLen+1, numHashBytes)
	if err != nil {
		return fmt.Errorf("lzma.NewWriterSizeLevel failed to create bin tree: %w", err)
	}

	z.optimum = make([]*optimal, kNumOpts)
	for i := range kNumOpts {
		z.optimum[i] = &optimal{}
	}

	z.isMatch = initBitModels(kNumStates << kNumPosStatesBitsMax)
	z.isRep = initBitModels(kNumStates)
	z.isRepG0 = initBitModels(kNumStates)
	z.isRepG1 = initBitModels(kNumStates)
	z.isRepG2 = initBitModels(kNumStates)
	z.isRep0Long = initBitModels(kNumStates << kNumPosStatesBitsMax)

	z.posSlotCoders = make([]*rangeBitTreeCoder, kNumLenToPosStates)
	for i := range kNumLenToPosStates {
		z.posSlotCoders[i] = newRangeBitTreeCoder(kNumPosSlotBits)
	}

	z.posCoders = initBitModels(kNumFullDistances - kEndPosModelIndex)
	z.posAlignCoder = newRangeBitTreeCoder(kNumAlignBits)

	z.lenCoder = newLenPriceTableCoder(z.cl.fastBytes+1-kMatchMinLen, 1<<z.cl.posStateBits)
	z.repMatchLenCoder = newLenPriceTableCoder(z.cl.fastBytes+1-kMatchMinLen, 1<<z.cl.posStateBits)

	z.litCoder = newLitCoder(z.cl.litPosStateBits, z.cl.litContextBits)

	z.matchDistances = make([]uint32, kMatchMaxLen*2+2)

	z.additionalOffset = 0

	z.optimumEndIndex = 0
	z.optimumCurrentIndex = 0

	z.longestMatchFound = false

	z.posSlotPrices = make([]uint32, 1<<(kNumPosSlotBits+kNumLenToPosStatesBits))
	z.distancesPrices = make([]uint32, kNumFullDistances<<kNumLenToPosStatesBits)
	z.alignPrices = make([]uint32, kAlignTableSize)

	z.posStateMask = 1<<z.cl.posStateBits - 1

	z.nowPos = 0
	z.finished = false

	z.state = 0
	z.prevByte = 0

	z.repDistances = make([]uint32, kNumRepDistances)
	for i := range kNumRepDistances {
		z.repDistances[i] = 0
	}

	z.matchPriceCount = 0

	z.reps = make([]uint32, kNumRepDistances)
	z.repLens = make([]uint32, kNumRepDistances)

	z.fillDistancesPrices()
	z.fillAlignPrices()

	z.doEncode()

	return
}

// local error wrapper so we can distinguish between error we want
// to return as errors from genuine panics.
type osError struct{ error }

// Report error and stop executing. Wraps error an osError for handlePanics() to
// distinguish them from genuine panics.
func throw(err error) {
	panic(&osError{err})
}

// handlePanics is a deferred function to turn a panic with type *osError into a plain error
// return. Other panics are unexpected and so are re-enabled.
func handlePanics(err *error) {
	if v := recover(); v != nil {
		switch e := v.(type) {
		case *osError:
			*err = e.error
		default:
			// runtime errors should crash
			panic(v)
		}
	}
}
