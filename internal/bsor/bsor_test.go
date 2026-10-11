package bsor_test

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/yyewolf/ssarchiver/internal/bsor"
)

// fixture emits a synthetic BSOR v1 file with the §2.1 layout (little-endian;
// strings as [int32 length][utf8 bytes]), followed by any afterInfo bytes.
func fixture(t *testing.T, ts int64, playerID, hash, songName, mapper, difficulty, mode string, score int64, failTime float64, afterInfo ...[]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := func(v any) {
		if err := binary.Write(&buf, binary.LittleEndian, v); err != nil {
			t.Fatal(err)
		}
	}
	str := func(s string) {
		w(int32(len(s)))
		buf.WriteString(s)
	}
	w(int32(0x442d3d69))
	buf.WriteByte(1) // version
	buf.WriteByte(0) // info section
	str("0.10.0")
	str("1.42.3_15380")
	str(strconv.FormatInt(ts, 10))
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
	w(int32(score))
	str(mode)
	str("DefaultEnvironment")
	str("")
	w(float32(20.5))
	buf.WriteByte(0)
	w(float32(0))
	w(float32(0))
	w(float32(failTime))
	w(float32(0))
	for _, b := range afterInfo {
		buf.Write(b)
	}
	return buf.Bytes()
}

// framesStub is a well-formed frames section the parser must never reach.
func framesStub() []byte {
	b := []byte{1}
	b = binary.LittleEndian.AppendUint32(b, 1)
	return append(b, make([]byte, 36)...)
}

func TestParseHeaderDecodesAllFields(t *testing.T) {
	f := fixture(t, 1791712056, "76561198038925092",
		"57511EE48555E00E031BD3B1DF90BA7BE5712B56", "Night Raid with a Dragon",
		"nolan121405", "ExpertPlus", "Standard", 426137, 67.91858, framesStub())
	h, err := bsor.ParseHeader(bytes.NewReader(f))
	if err != nil {
		t.Fatal(err)
	}
	want := bsor.Header{
		PlayerID:   "76561198038925092",
		PlayerName: "Yewolf",
		Timestamp:  1791712056,
		Hash:       "57511EE48555E00E031BD3B1DF90BA7BE5712B56", // hash keeps its uppercase
		SongName:   "Night Raid with a Dragon",
		Mapper:     "nolan121405",
		Difficulty: "ExpertPlus",
		Mode:       "Standard",
		Score:      426137,
		FailTime:   float64(float32(67.91858)),
	}
	if h != want {
		t.Fatalf("header = %+v, want %+v", h, want)
	}
}

func TestParseHeaderRejectsGarbage(t *testing.T) {
	valid := fixture(t, 1791712056, "76561198038925092",
		"57511EE48555E00E031BD3B1DF90BA7BE5712B56", "Night Raid with a Dragon",
		"nolan121405", "ExpertPlus", "Standard", 426137, 67.91858)

	wrongMagic := bytes.Clone(valid)
	binary.LittleEndian.PutUint32(wrongMagic, 0x11223344)

	wrongVersion := bytes.Clone(valid)
	wrongVersion[4] = 2

	sectionNotInfo := bytes.Clone(valid)
	sectionNotInfo[5] = 1

	oversizeString := bytes.Clone(valid)
	binary.LittleEndian.PutUint32(oversizeString[6:], 2000) // the first string's length prefix

	invalidUTF8 := fixture(t, 1791712056, "76561198038925092",
		"57511EE48555E00E031BD3B1DF90BA7BE5712B56", "\xff\xfe",
		"nolan121405", "ExpertPlus", "Standard", 426137, 67.91858) // songName is not UTF-8

	hugeLength := bytes.Clone(valid)
	binary.LittleEndian.PutUint32(hugeLength[6:], 0xFFFFFFFF) // huge as uint32, negative as int32

	for _, tc := range []struct {
		name string
		data []byte
	}{
		{"wrong magic", wrongMagic},
		{"wrong version byte", wrongVersion},
		{"first section is not info", sectionNotInfo},
		{"truncated in the middle of a string", valid[:13]}, // inside "0.10.0"
		{"string length over 1024", oversizeString},
		{"string is not valid UTF-8", invalidUTF8},
		{"string length 0xFFFFFFFF", hugeLength},
	} {
		if _, err := bsor.ParseHeader(bytes.NewReader(tc.data)); err == nil {
			t.Errorf("%s: ParseHeader must fail", tc.name)
		}
	}
}

// TestParseHeaderStringBoundary pins the string length guard's inclusive
// bound: maxStringLen bytes parse, one more is garbage.
func TestParseHeaderStringBoundary(t *testing.T) {
	atLimit := fixture(t, 1791712056, "76561198038925092",
		"57511EE48555E00E031BD3B1DF90BA7BE5712B56", strings.Repeat("a", 1024),
		"nolan121405", "ExpertPlus", "Standard", 426137, 67.91858)
	h, err := bsor.ParseHeader(bytes.NewReader(atLimit))
	if err != nil {
		t.Fatal(err)
	}
	if h.SongName != strings.Repeat("a", 1024) {
		t.Fatalf("1024-byte string did not round-trip: %d bytes", len(h.SongName))
	}

	overLimit := fixture(t, 1791712056, "76561198038925092",
		"57511EE48555E00E031BD3B1DF90BA7BE5712B56", strings.Repeat("a", 1025),
		"nolan121405", "ExpertPlus", "Standard", 426137, 67.91858)
	if _, err := bsor.ParseHeader(bytes.NewReader(overLimit)); err == nil {
		t.Fatal("a 1025-byte string must fail")
	}
}

func TestFilenameEndType(t *testing.T) {
	for _, tok := range []string{"fail", "quit", "restart", "practice"} {
		name := "76561198038925092-" + tok + "-X-ExpertPlus-Standard-H1-1791712056.bsor"
		if et, ok := bsor.FilenameEndType(name); et != tok || !ok {
			t.Errorf("FilenameEndType(%q) = (%q, %v), want (%q, true)", name, et, ok, tok)
		}
	}
	if et, ok := bsor.FilenameEndType("76561198038925092-quit-fail-x.bsor"); et != "quit" || !ok {
		t.Errorf("FilenameEndType = (%q, %v), want (quit, true)", et, ok)
	}
	for _, name := range []string{
		"76561198038925092-X-ExpertPlus-Standard-H1-1.bsor",
		"Yewolf-fail-x.bsor", // no leading digits
	} {
		if et, ok := bsor.FilenameEndType(name); et != "" || ok {
			t.Errorf("FilenameEndType(%q) = (%q, %v), want (\"\", false)", name, et, ok)
		}
	}
}

// TestRealSamples parses real replays from SSA_BSOR_SAMPLES (not committed);
// it is skipped when the variable is unset.
func TestRealSamples(t *testing.T) {
	dir := os.Getenv("SSA_BSOR_SAMPLES")
	if dir == "" {
		t.Skip("set SSA_BSOR_SAMPLES to a directory of .bsor replays")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	timeset := regexp.MustCompile(`-([0-9]+)\.bsor$`)
	n := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".bsor") {
			continue
		}
		n++
		t.Run(name, func(t *testing.T) {
			f, err := os.Open(filepath.Join(dir, name))
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			h, err := bsor.ParseHeader(f)
			if err != nil {
				t.Fatal(err)
			}
			if h.PlayerID == "" || h.Hash == "" {
				t.Fatalf("empty PlayerID/Hash: %+v", h)
			}
			if h.Timestamp <= 1_500_000_000 {
				t.Fatalf("implausible timestamp %d: %+v", h.Timestamp, h)
			}
			if m := timeset.FindStringSubmatch(name); m != nil {
				if ts, _ := strconv.ParseInt(m[1], 10, 64); ts != h.Timestamp {
					t.Fatalf("filename timeset %d != header timestamp %d", ts, h.Timestamp)
				}
			}
			if strings.Contains(name, "-fail-") && h.FailTime <= 0 {
				t.Fatalf("fail file must have FailTime > 0: %+v", h)
			}
		})
	}
	if n == 0 {
		t.Fatalf("no .bsor files in %s", dir)
	}
}
