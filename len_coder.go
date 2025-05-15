package lzma

type lenCoder struct {
	choice    []uint16
	lowCoder  []*rangeBitTreeCoder
	midCoder  []*rangeBitTreeCoder
	highCoder *rangeBitTreeCoder
}

func newLenCoder(
	numPosStates uint32, /*1 << pb*/
) *lenCoder {
	lc := &lenCoder{
		choice:    initBitModels(2),
		lowCoder:  make([]*rangeBitTreeCoder, kNumPosStatesMax),
		midCoder:  make([]*rangeBitTreeCoder, kNumPosStatesMax),
		highCoder: newRangeBitTreeCoder(kNumHighLenBits),
	}
	var i uint32
	for ; i < numPosStates; i++ {
		lc.lowCoder[i] = newRangeBitTreeCoder(kNumLowLenBits)
		lc.midCoder[i] = newRangeBitTreeCoder(kNumMidLenBits)
	}

	return lc
}

func (lc *lenCoder) decode(rd *rangeDecoder, posState uint32) (uint32, error) {
	var res, k uint32
	i, err := rd.decodeBit(lc.choice, 0)
	if err != nil {
		return 0, err
	}
	if i == 0 {
		res, err = lc.lowCoder[posState].decode(rd)
		if err != nil {
			return 0, err
		}

		return res, nil
	}
	res = kNumLowLenSymbols
	j, err := rd.decodeBit(lc.choice, 1)
	if err != nil {
		return 0, err
	}
	if j == 0 {
		k, err = lc.midCoder[posState].decode(rd)
		if err != nil {
			return 0, err
		}
		res += k

		return res, nil
	}
	l, err := lc.highCoder.decode(rd)
	if err != nil {
		return 0, err
	}
	res = res + kNumMidLenSymbols + l

	return res, nil
}

func (lc *lenCoder) encode(re *rangeEncoder, symbol, posState uint32) {
	if symbol < kNumLowLenSymbols {
		re.encode(lc.choice, 0, 0)
		lc.lowCoder[posState].encode(re, symbol)
	} else {
		symbol -= kNumLowLenSymbols
		re.encode(lc.choice, 0, 1)
		if symbol < kNumMidLenSymbols {
			re.encode(lc.choice, 1, 0)
			lc.midCoder[posState].encode(re, symbol)
		} else {
			re.encode(lc.choice, 1, 1)
			lc.highCoder.encode(re, symbol-kNumMidLenSymbols)
		}
	}
}

// write prices into prices []uint32.
func (lc *lenCoder) setPrices(prices []uint32, posState, numSymbols, st uint32) {
	a0 := getPrice0(lc.choice[0])
	a1 := getPrice1(lc.choice[0])
	b0 := a1 + getPrice0(lc.choice[1])
	b1 := a1 + getPrice1(lc.choice[1])

	var i uint32
	for ; i < kNumLowLenSymbols; i++ {
		if i >= numSymbols {
			return
		}
		prices[st+i] = a0 + lc.lowCoder[posState].getPrice(i)
	}
	i = kNumLowLenSymbols
	for ; i < kNumLowLenSymbols+kNumMidLenSymbols; i++ {
		if i >= numSymbols {
			return
		}
		prices[st+i] = b0 + lc.midCoder[posState].getPrice(i-kNumLowLenSymbols)
	}
	for ; i < numSymbols; i++ {
		prices[st+i] = b1 + lc.highCoder.getPrice(i-kNumLowLenSymbols-kNumMidLenSymbols)
	}
}

type lenPriceTableCoder struct {
	lc        *lenCoder
	prices    []uint32
	counters  []uint32
	tableSize uint32
}

func newLenPriceTableCoder(tableSize, numPosStates uint32) *lenPriceTableCoder {
	pc := &lenPriceTableCoder{
		lc:        newLenCoder(numPosStates),
		prices:    make([]uint32, kNumLenSymbols<<kNumPosStatesBitsMax),
		counters:  make([]uint32, kNumPosStatesMax),
		tableSize: tableSize,
	}
	var posState uint32
	for ; posState < numPosStates; posState++ {
		pc.updateTable(posState)
	}

	return pc
}

func (pc *lenPriceTableCoder) updateTable(posState uint32) {
	pc.lc.setPrices(pc.prices, posState, pc.tableSize, posState*kNumLenSymbols)
	pc.counters[posState] = pc.tableSize
}

func (pc *lenPriceTableCoder) getPrice(symbol, posState uint32) uint32 {
	return pc.prices[posState*kNumLenSymbols+symbol]
}

func (pc *lenPriceTableCoder) encode(re *rangeEncoder, symbol, posState uint32) {
	pc.lc.encode(re, symbol, posState)
	pc.counters[posState]--
	if pc.counters[posState] == 0 {
		pc.updateTable(posState)
	}
}
