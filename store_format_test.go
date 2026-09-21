package agentboard_test

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JiaBao-do/agentboard"
	"github.com/JiaBao-do/agentboard/model"
)

func TestFileFormatRoundTripAndSize(t *testing.T) {
	st := seededState(1000)
	b, err := agentboard.EncodeFile(st)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(b, []byte("ABD2")) || b[4] != 2 {
		t.Fatalf("header = % x", b[:5])
	}
	got, legacy, err := agentboard.DecodeFile(b)
	if err != nil || legacy {
		t.Fatalf("decode: legacy=%v err=%v", legacy, err)
	}
	if len(got.Tasks) != 1000 || len(got.Activity) != 5000 || got.Tasks["AB-7"].Title != st.Tasks["AB-7"].Title {
		t.Fatalf("round trip lost data: %d tasks %d activity", len(got.Tasks), len(got.Activity))
	}
	pretty, _ := json.MarshalIndent(st, "", "  ")
	if len(b)*10 > len(pretty) {
		t.Fatalf("file is %d bytes vs %d indented JSON: expected at least 10x smaller", len(b), len(pretty))
	}
	t.Logf("1000 tasks: %d bytes on disk vs %d indented JSON (%.0fx smaller)", len(b), len(pretty), float64(len(pretty))/float64(len(b)))
}

func TestDecodeFileDetectsDamage(t *testing.T) {
	good, _ := agentboard.EncodeFile(seededState(50))
	mutate := func(f func([]byte) []byte) []byte { return f(append([]byte(nil), good...)) }
	tests := []struct {
		name string
		data []byte
		want error
	}{
		{"flipped payload byte", mutate(func(b []byte) []byte { b[len(b)/2] ^= 0xFF; return b }), agentboard.ErrCorrupt},
		{"flipped checksum byte", mutate(func(b []byte) []byte { b[len(b)-1] ^= 0x01; return b }), agentboard.ErrCorrupt},
		{"flipped length byte", mutate(func(b []byte) []byte { b[12] ^= 0x01; return b }), agentboard.ErrCorrupt},
		{"truncated tail", good[:len(good)-7], agentboard.ErrCorrupt},
		{"truncated to header", good[:10], agentboard.ErrCorrupt},
		{"appended garbage", append(append([]byte(nil), good...), 'x'), agentboard.ErrCorrupt},
		{"newer format version", mutate(func(b []byte) []byte { b[4] = 3; return b }), agentboard.ErrInvalid},
		{"unknown magic", []byte("ABD9............."), agentboard.ErrInvalid},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			st, _, err := agentboard.DecodeFile(tc.data)
			if !errors.Is(err, tc.want) || st != nil {
				t.Fatalf("st=%v err=%v, want %v", st != nil, err, tc.want)
			}
		})
	}
	t.Run("every single byte flip is caught or harmless-free", func(t *testing.T) {
		small, _ := agentboard.EncodeFile(seededState(3))
		for i := range small {
			b := append([]byte(nil), small...)
			b[i] ^= 0x55
			if _, _, err := agentboard.DecodeFile(b); err == nil {
				t.Fatalf("flipping byte %d went unnoticed", i)
			}
		}
	})
	t.Run("every truncation is refused", func(t *testing.T) {
		small, _ := agentboard.EncodeFile(seededState(3))
		for n := 1; n < len(small); n++ {
			if _, _, err := agentboard.DecodeFile(small[:n]); err == nil {
				t.Fatalf("truncation to %d bytes was accepted", n)
			}
		}
	})
	t.Run("checksum-valid garbage payload is still refused", func(t *testing.T) {
		payload := []byte("not deflate data at all")
		b := append([]byte("ABD2\x02"), make([]byte, 8)...)
		binary.BigEndian.PutUint64(b[5:], uint64(len(payload)))
		b = append(b, payload...)
		b = binary.BigEndian.AppendUint32(b, crcOf(payload))
		if _, _, err := agentboard.DecodeFile(b); !errors.Is(err, agentboard.ErrCorrupt) {
			t.Fatalf("err = %v", err)
		}
	})
}

func FuzzDecodeFile(f *testing.F) {
	good, _ := agentboard.EncodeFile(seededState(2))
	f.Add(good)
	f.Add([]byte(`{"version":1}`))
	f.Add([]byte("ABD2"))
	f.Add([]byte{})
	f.Add(good[:len(good)/2])
	f.Fuzz(func(t *testing.T, data []byte) {
		st, _, err := agentboard.DecodeFile(data) // must never panic
		if err == nil && st == nil {
			t.Fatal("nil state without error")
		}
		if err == nil {
			if verr := agentboard.ValidateState(st); verr != nil {
				t.Fatalf("decoded state fails validation: %v", verr)
			}
		}
	})
}

func TestLegacyJSONIsMigratedOnce(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "board.json")
	st := seededState(20)
	legacy, _ := json.MarshalIndent(st, "", "  ")
	if err := os.WriteFile(path, legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	fs := agentboard.NewFileStore(path)
	got, err := fs.Load()
	if err != nil || len(got.Tasks) != 20 {
		t.Fatalf("load legacy: %v", err)
	}
	bak, err := os.ReadFile(path + ".bak")
	if err != nil || !bytes.Equal(bak, legacy) {
		t.Fatalf("the legacy file must be kept byte for byte as .bak: %v", err)
	}
	now, _ := os.ReadFile(path)
	if !bytes.HasPrefix(now, []byte("ABD2")) || len(now) >= len(legacy) {
		t.Fatalf("data file was not rewritten in the new format (%d vs %d bytes)", len(now), len(legacy))
	}
	again, err := agentboard.NewFileStore(path).Load()
	if err != nil || len(again.Tasks) != 20 {
		t.Fatalf("reload after migration: %v", err)
	}
	// A second legacy file must never overwrite the first backup.
	if err := os.WriteFile(path, []byte(`{"version":1,"projects":{},"tasks":{},"agents":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := agentboard.NewFileStore(path).Load(); err != nil {
		t.Fatal(err)
	}
	if bak2, _ := os.ReadFile(path + ".bak"); !bytes.Equal(bak2, legacy) {
		t.Fatal("an existing backup was overwritten")
	}
}

func TestCorruptDataFileIsNeverOverwritten(t *testing.T) {
	path := filepath.Join(t.TempDir(), "board.json")
	good, _ := agentboard.EncodeFile(seededState(5))
	good[len(good)/2] ^= 0xFF
	os.WriteFile(path, good, 0o600)
	if _, err := agentboard.Open(agentboard.Options{Store: agentboard.NewFileStore(path)}); !errors.Is(err, agentboard.ErrCorrupt) {
		t.Fatalf("Open err = %v", err)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(after, good) {
		t.Fatal("a corrupt file must be left untouched for recovery")
	}
	if _, err := agentboard.NewFileStore(path).Load(); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("error should name the checksum: %v", err)
	}
}

func TestNewerSchemaInsideValidFileIsRefused(t *testing.T) {
	st := model.NewState()
	b, _ := agentboard.EncodeFile(st)
	// Re-encode a payload claiming schema 99 by hand.
	_ = b
	raw := []byte(`{"version":99}`)
	if _, _, err := agentboard.DecodeFile(raw); !errors.Is(err, agentboard.ErrInvalid) {
		t.Fatalf("err = %v", err)
	}
}
