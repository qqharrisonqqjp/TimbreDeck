package main

import (
	"encoding/binary"
	"math"
)

// A deterministic speech-band probe for warm-up, not a recorded voice.
func syntheticSTSCalibration() []byte {
	const rate, frames = 16000, 3 * 16000
	b := make([]byte, 44+frames*2)
	copy(b, "RIFF")
	binary.LittleEndian.PutUint32(b[4:], uint32(len(b)-8))
	copy(b[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(b[16:], 16)
	binary.LittleEndian.PutUint16(b[20:], 1)
	binary.LittleEndian.PutUint16(b[22:], 1)
	binary.LittleEndian.PutUint32(b[24:], rate)
	binary.LittleEndian.PutUint32(b[28:], rate*2)
	binary.LittleEndian.PutUint16(b[32:], 2)
	binary.LittleEndian.PutUint16(b[34:], 16)
	copy(b[36:], "data")
	binary.LittleEndian.PutUint32(b[40:], frames*2)
	for i := 0; i < frames; i++ {
		t := float64(i) / rate
		phase := 2 * math.Pi * (156*t + 8*math.Sin(t))
		gate := math.Pow(math.Sin(math.Pi*t*3), 2)
		x := 3000 * gate * (math.Sin(phase) + .35*math.Sin(3*phase) + .18*math.Sin(5*phase))
		binary.LittleEndian.PutUint16(b[44+2*i:], uint16(int16(x)))
	}
	return b
}
