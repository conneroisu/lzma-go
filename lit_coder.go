package lzma

type litSubCoder struct {
	coders []uint16
}

func newLitSubCoder() *litSubCoder {
	return &litSubCoder{
		coders: initBitModels(0x300),
	}
}

func (lsc *litSubCoder) decodeNormal(rd *rangeDecoder) (byte, error) {
	symbol := uint32(1)
	for symbol < 0x100 {
		i, err := rd.decodeBit(lsc.coders, symbol)
		if err != nil {
			return 0, err
		}
		symbol = symbol<<1 | i
	}

	return byte(symbol), nil
}

func (lsc *litSubCoder) decodeWithMatchByte(
	rd *rangeDecoder,
	matchByte byte,
) (byte, error) {
	uMatchByte := uint32(matchByte)
	symbol := uint32(1)
	for symbol < 0x100 {
		matchBit := (uMatchByte >> 7) & 1
		uMatchByte <<= 1
		bit, err := rd.decodeBit(lsc.coders, ((1+matchBit)<<8)+symbol)
		if err != nil {
			return 0, err
		}
		symbol = (symbol << 1) | bit
		if matchBit != bit {
			for symbol < 0x100 {
				i, err := rd.decodeBit(lsc.coders, symbol)
				if err != nil {
					return 0, err
				}
				symbol = (symbol << 1) | i
			}

			break
		}
	}

	return byte(symbol), nil
}

func (lsc *litSubCoder) encode(re *rangeEncoder, symbol byte) {
	uSymbol := uint32(symbol)
	context := uint32(1)
	for i := uint32(7); int32(i) >= 0; i-- {
		bit := (uSymbol >> i) & 1
		re.encode(lsc.coders, context, bit)
		context = context<<1 | bit
	}
}

func (lsc *litSubCoder) encodeMatched(
	re *rangeEncoder,
	matchByte, symbol byte,
) {
	uMatchByte := uint32(matchByte)
	uSymbol := uint32(symbol)
	context := uint32(1)
	same := true
	for i := uint32(7); int32(i) >= 0; i-- {
		bit := (uSymbol >> i) & 1
		state := context
		if same {
			matchBit := (uMatchByte >> i) & 1
			state += (1 + matchBit) << 8
			same = false
			if matchBit == bit {
				same = true
			}
		}
		re.encode(lsc.coders, state, bit)
		context = context<<1 | bit
	}
}

func (lsc *litSubCoder) getPrice(matchMode bool, matchByte, symbol byte) uint32 {
	var price uint32
	uMatchByte := uint32(matchByte)
	uSymbol := uint32(symbol)
	context := uint32(1)
	i := uint32(7)
	if matchMode {
		for ; int32(i) >= 0; i-- {
			matchBit := (uMatchByte >> i) & 1
			bit := (uSymbol >> i) & 1
			price += getPrice(lsc.coders[(1+matchBit)<<8+context], bit)
			context = context<<1 | bit
			if matchBit != bit {
				i--

				break
			}
		}
	}
	for ; int32(i) >= 0; i-- {
		bit := (uSymbol >> i) & 1
		price += getPrice(lsc.coders[context], bit)
		context = context<<1 | bit
	}

	return price
}

type litCoder struct {
	coders      []*litSubCoder
	numPrevBits uint32 // literal context bits // lc
	posMask     uint32
}

func newLitCoder(numPosBits, numPrevBits uint32) *litCoder {
	numStates := uint32(1) << (numPrevBits + numPosBits)
	lc := &litCoder{
		coders:      make([]*litSubCoder, numStates),
		numPrevBits: numPrevBits,
		posMask:     (1 << numPosBits) - 1,
	}
	var i uint32
	for ; i < numStates; i++ {
		lc.coders[i] = newLitSubCoder()
	}

	return lc
}

func (lc *litCoder) getSubCoder(pos uint32, prevByte byte) *litSubCoder {
	return lc.coders[((pos&lc.posMask)<<lc.numPrevBits)+uint32(prevByte>>(8-lc.numPrevBits))]
}
