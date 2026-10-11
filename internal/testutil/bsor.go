package testutil

import (
	"bytes"
	"encoding/binary"
	"math"
	"strconv"
)

// i32 narrows n to the BSOR int32 fields (fixture values are small).
func i32(n int64) int32 {
	if n < math.MinInt32 || n > math.MaxInt32 {
		panic("testutil: value out of int32 range")
	}
	return int32(n)
}

// BSORReplay builds a minimal BSOR v1 file (info + a one-frame stub) with the
// given info values; it mirrors the fixture layout of internal/bsor's tests.
func BSORReplay(playerID, hash, songName, mapper, difficulty, mode string, score int64, timestamp int64, failTime float64) []byte {
	var buf bytes.Buffer
	w := func(v any) { _ = binary.Write(&buf, binary.LittleEndian, v) }
	str := func(s string) {
		w(i32(int64(len(s))))
		buf.WriteString(s)
	}
	w(int32(0x442d3d69))
	buf.WriteByte(1) // version
	buf.WriteByte(0) // info section
	str("0.10.0")
	str("1.42.3_15380")
	str(strconv.FormatInt(timestamp, 10))
	str(playerID)
	str("Yewolf")
	str("steam")
	str("OpenXR")
	str(`"Valve""Deckard MP"`)
	str("Index Controller OpenXR")
	str(hash)
	str(songName)
	str(mapper)
	str(difficulty)
	w(i32(score))
	str(mode)
	str("DefaultEnvironment")
	str("")
	w(float32(20.5))
	buf.WriteByte(0)
	w(float32(0))
	w(float32(0))
	w(float32(failTime))
	w(float32(0))
	frames := []byte{1} // frames section stub the parser must never reach
	frames = binary.LittleEndian.AppendUint32(frames, 1)
	frames = append(frames, make([]byte, 36)...)
	return append(buf.Bytes(), frames...)
}
