package sign

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

var testKey = Key(bytes.Repeat([]byte{7}, keySize))

func TestSealOpen(t *testing.T) {
	body := []byte(`{"rules": []}`)
	sealed := testKey.Seal("index", body)
	got, err := testKey.Open("index", sealed)
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("Open = %q, %v; want the body", got, err)
	}

	other := Key(bytes.Repeat([]byte{8}, keySize))
	tampered := append(bytes.Clone(sealed[:len(sealed)-1]), '!')
	tests := []struct {
		name    string
		key     Key
		purpose string
		data    []byte
	}{
		{"unsigned", testKey, "index", body},
		{"another machine's key", other, "index", sealed},
		{"another purpose", testKey, "session:s1", sealed},
		{"edited body", testKey, "index", tampered},
		{"no key", nil, "index", sealed},
		{"empty", testKey, "index", nil},
		{"header only", testKey, "index", []byte(header + "00\n")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := tt.key.Open(tt.purpose, tt.data); !errors.Is(err, ErrBadSeal) {
				t.Errorf("Open: err = %v, want ErrBadSeal", err)
			}
		})
	}
}

func TestLoadKey(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "stele")
	k1, err := LoadKey(dir)
	if err != nil {
		t.Fatalf("first LoadKey: %v", err)
	}
	if len(k1) != keySize {
		t.Fatalf("key is %d bytes, want %d", len(k1), keySize)
	}
	k2, err := LoadKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(k1, k2) {
		t.Error("a second LoadKey made a new key")
	}

	if runtime.GOOS != "windows" {
		info, err := os.Stat(filepath.Join(dir, keyFile))
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Errorf("key mode = %v, want 0600", got)
		}
	}
}

// Processes creating the key at once agree on one.
func TestLoadKeyConcurrent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "stele")
	const n = 10
	keys := make([]Key, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			keys[i], errs[i] = LoadKey(dir)
		}()
	}
	wg.Wait()
	for i := range n {
		if errs[i] != nil {
			t.Fatalf("LoadKey: %v", errs[i])
		}
		if !bytes.Equal(keys[i], keys[0]) {
			t.Fatal("concurrent LoadKey calls returned different keys")
		}
	}
}

// A key that cannot be read fails loudly; stele never falls back to trusting
// unsigned files.
func TestLoadKeyCorrupt(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, keyFile), []byte("not hex\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadKey(dir); err == nil {
		t.Error("LoadKey accepted a corrupt key")
	}
}

// LoadExistingKey never makes a key or its directory: stele check in CI
// must leave the machine as it found it.
func TestLoadExistingKeyMissingCreatesNothing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "stele")
	if _, err := LoadExistingKey(dir); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("err = %v, want ErrNotExist", err)
	}
	if _, err := os.Lstat(dir); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("LoadExistingKey created %s", dir)
	}
}

func TestLoadExistingKeyReadsKey(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "stele")
	want, err := LoadKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := LoadExistingKey(dir)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("LoadExistingKey = %x, %v; want the key LoadKey made", got, err)
	}
	if err := os.WriteFile(filepath.Join(dir, keyFile), []byte("not hex\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadExistingKey(dir); err == nil {
		t.Error("LoadExistingKey accepted a corrupt key")
	}
}

func TestMACVerify(t *testing.T) {
	body := []byte(`{"v":1}`)
	mac := testKey.MAC("log", body)
	if len(mac) != 64 {
		t.Fatalf("MAC is %d hex digits, want 64", len(mac))
	}
	if !testKey.Verify("log", body, mac) {
		t.Fatal("Verify refused its own MAC")
	}
	other := Key(bytes.Repeat([]byte{8}, keySize))
	tests := []struct {
		name    string
		key     Key
		purpose string
		body    []byte
		mac     string
	}{
		{"another purpose", testKey, "index", body, mac},
		{"another machine's key", other, "log", body, mac},
		{"no key", nil, "log", body, mac},
		{"edited body", testKey, "log", []byte(`{"v":2}`), mac},
		{"not hex", testKey, "log", body, "zz"},
		{"empty", testKey, "log", body, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.key.Verify(tt.purpose, tt.body, tt.mac) {
				t.Error("Verify accepted it")
			}
		})
	}
}
