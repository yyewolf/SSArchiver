// Package bsor decodes the header (info section) of BSOR v1 replay files,
// the native format of BeatLeader replays (spec §2.1).
package bsor

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"strconv"
	"unicode/utf8"
)

const (
	magic        = 0x442d3d69
	maxStringLen = 1024
)

// Header is the decoded info section of a BSOR v1 replay (spec §2.1).
type Header struct {
	PlayerID, PlayerName        string
	Timestamp                   int64 // unix seconds; the run's START (spec §2.2)
	Hash, SongName, Mapper      string
	Difficulty, Mode, Modifiers string
	Score                       int64
	StartTime, FailTime, Speed  float64
}

// ParseHeader decodes the info section of a BSOR v1 replay (spec §2.1).
// It reads only up to the end of info; frames/notes are never parsed.
func ParseHeader(r io.Reader) (Header, error) {
	b := bufio.NewReader(r)
	var head [4]byte
	if _, err := io.ReadFull(b, head[:]); err != nil {
		return Header{}, err
	}
	if m := binary.LittleEndian.Uint32(head[:]); m != magic {
		return Header{}, fmt.Errorf("bsor: bad magic %#x", m)
	}
	ver, err := b.ReadByte()
	if err != nil {
		return Header{}, err
	}
	if ver != 1 {
		return Header{}, fmt.Errorf("bsor: unsupported version %d", ver)
	}
	section, err := b.ReadByte()
	if err != nil {
		return Header{}, err
	}
	if section != 0 {
		return Header{}, fmt.Errorf("bsor: first section is type %d, want 0 (info)", section)
	}

	d := infoDecoder{b: b}
	d.str() // format version
	d.str() // gameVersion
	ts := d.str()
	playerID := d.str()
	playerName := d.str()
	d.str() // platform
	d.str() // trackingSystem
	d.str() // hmd
	d.str() // controller
	hash := d.str()
	songName := d.str()
	mapper := d.str()
	difficulty := d.str()
	score := d.i32()
	mode := d.str()
	d.str() // environment
	modifiers := d.str()
	d.f32() // jumpDistance
	d.u8()  // leftHanded
	d.f32() // height
	startTime := d.f32()
	failTime := d.f32()
	speed := d.f32()
	if d.err != nil {
		return Header{}, d.err
	}
	timestamp, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return Header{}, fmt.Errorf("bsor: bad timestamp %q", ts)
	}
	return Header{
		PlayerID:   playerID,
		PlayerName: playerName,
		Timestamp:  timestamp,
		Hash:       hash,
		SongName:   songName,
		Mapper:     mapper,
		Difficulty: difficulty,
		Mode:       mode,
		Modifiers:  modifiers,
		Score:      int64(score),
		StartTime:  startTime,
		FailTime:   failTime,
		Speed:      speed,
	}, nil
}

// endTypeRe matches the mod's filename prefix: a player ID followed by an
// optional end-type token (spec §2.1).
var endTypeRe = regexp.MustCompile(`^[0-9]{1,32}-(fail|quit|restart|practice)-`)

// FilenameEndType extracts the mod's end-type token: the optional
// fail|quit|restart|practice that follows the leading player ID
// (spec §2.1). ok is false when the name has no recognizable token.
func FilenameEndType(name string) (endType string, ok bool) {
	if m := endTypeRe.FindStringSubmatch(name); m != nil {
		return m[1], true
	}
	return "", false
}

// infoDecoder reads the fields of an info section in §2.1 order, keeping the
// first error it meets.
type infoDecoder struct {
	b   *bufio.Reader
	err error
}

func (d *infoDecoder) str() string {
	if d.err != nil {
		return ""
	}
	var n [4]byte
	if _, err := io.ReadFull(d.b, n[:]); err != nil {
		d.err = err
		return ""
	}
	// The length is unsigned in the format; anything over the header's
	// realistic string bound (including values over MaxInt32) is garbage.
	size := binary.LittleEndian.Uint32(n[:])
	if size > maxStringLen {
		d.err = fmt.Errorf("bsor: string length %d out of range", size)
		return ""
	}
	s := make([]byte, size)
	if _, err := io.ReadFull(d.b, s); err != nil {
		d.err = err
		return ""
	}
	if !utf8.Valid(s) {
		d.err = errors.New("bsor: string is not valid UTF-8")
		return ""
	}
	return string(s)
}

func (d *infoDecoder) i32() int32 {
	if d.err != nil {
		return 0
	}
	var buf [4]byte
	if _, err := io.ReadFull(d.b, buf[:]); err != nil {
		d.err = err
		return 0
	}
	u := binary.LittleEndian.Uint32(buf[:])
	if u > math.MaxInt32 { // the score field is a signed int32; anything else is corrupt
		d.err = fmt.Errorf("bsor: score %d out of range", u)
		return 0
	}
	return int32(u)
}

func (d *infoDecoder) f32() float64 {
	if d.err != nil {
		return 0
	}
	var buf [4]byte
	if _, err := io.ReadFull(d.b, buf[:]); err != nil {
		d.err = err
		return 0
	}
	return float64(math.Float32frombits(binary.LittleEndian.Uint32(buf[:])))
}

func (d *infoDecoder) u8() byte {
	if d.err != nil {
		return 0
	}
	v, err := d.b.ReadByte()
	if err != nil {
		d.err = err
		return 0
	}
	return v
}
