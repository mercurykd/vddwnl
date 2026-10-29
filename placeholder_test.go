package main

import (
	"bytes"
	"compress/flate"
	"encoding/binary"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// TestStatusPlaceholderPNG проверяет, что заглушка — валидный PNG: корректные
// чанки, совпадающие CRC, zlib-поток и размер распакованных строк.
func TestStatusPlaceholderPNG(t *testing.T) {
	data, err := statusPlaceholderPNG()
	if err != nil {
		t.Fatalf("encodePNG: %v", err)
	}

	sig := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}
	if !bytes.HasPrefix(data, sig) {
		t.Fatalf("нет PNG-сигнатуры: % x", data[:8])
	}

	var (
		gotW, gotH int
		seenIHDR   bool
		seenIDAT   bool
		seenIEND   bool
		idat       []byte
	)
	rest := data[len(sig):]
	for len(rest) > 0 {
		if len(rest) < 12 {
			t.Fatalf("обрезанный чанк: осталось %d байт", len(rest))
		}
		length := int(binary.BigEndian.Uint32(rest[0:4]))
		typ := string(rest[4:8])
		if len(rest) < 12+length {
			t.Fatalf("чанк %q объявляет %d байт, а данных меньше", typ, length)
		}
		payload := rest[8 : 8+length]
		want := binary.BigEndian.Uint32(rest[8+length : 12+length])
		h := crc32.NewIEEE()
		h.Write(rest[4 : 8+length])
		if got := h.Sum32(); got != want {
			t.Errorf("CRC чанка %q: получено %08x, ожидалось %08x", typ, got, want)
		}

		switch typ {
		case "IHDR":
			seenIHDR = true
			gotW = int(binary.BigEndian.Uint32(payload[0:4]))
			gotH = int(binary.BigEndian.Uint32(payload[4:8]))
			if payload[8] != 8 || payload[9] != 6 {
				t.Errorf("ожидался 8-битный RGBA, получено depth=%d color=%d", payload[8], payload[9])
			}
		case "IDAT":
			seenIDAT = true
			idat = append(idat, payload...)
		case "IEND":
			seenIEND = true
		}
		rest = rest[12+length:]
	}

	if !seenIHDR || !seenIDAT || !seenIEND {
		t.Fatalf("не все обязательные чанки: IHDR=%v IDAT=%v IEND=%v", seenIHDR, seenIDAT, seenIEND)
	}
	if gotW != statusImageWidth || gotH != statusImageHeight {
		t.Errorf("размер %dx%d, ожидался %dx%d", gotW, gotH, statusImageWidth, statusImageHeight)
	}

	if len(idat) < 6 {
		t.Fatalf("слишком короткий zlib-поток: %d байт", len(idat))
	}
	if idat[0] != 0x78 || idat[1] != 0x9c {
		t.Errorf("zlib-заголовок % x, ожидался 78 9c", idat[:2])
	}
	if got, want := adler32sum(nil), uint32(1); got != want {
		t.Errorf("adler32 пустых данных = %d, ожидалось %d", got, want)
	}
	if got, want := adler32sum([]byte("Wikipedia")), uint32(0x11E60398); got != want {
		t.Errorf("adler32 эталона = %08x, ожидалось %08x", got, want)
	}

	body := idat[2 : len(idat)-4]
	fr := flate.NewReader(bytes.NewReader(body))
	raw, err := io.ReadAll(fr)
	fr.Close()
	if err != nil {
		t.Fatalf("распаковка IDAT: %v", err)
	}
	wantSize := (statusImageWidth*4 + 1) * statusImageHeight
	if len(raw) != wantSize {
		t.Fatalf("распаковано %d байт, ожидалось %d", len(raw), wantSize)
	}
	sum := binary.BigEndian.Uint32(idat[len(idat)-4:])
	if got := adler32sum(raw); got != sum {
		t.Errorf("Adler-32 в потоке %08x, посчитанный %08x", sum, got)
	}

	// Оставляю копию, чтобы можно было проверить файл сторонним декодером.
	if dir := os.Getenv("TMP"); dir != "" {
		_ = os.WriteFile(filepath.Join(dir, "status-placeholder.png"), data, 0o644)
	}
}

func TestPlaceholderFile(t *testing.T) {
	dir := t.TempDir()
	path, err := writeStatusPlaceholder(dir)
	if err != nil {
		t.Fatalf("writeStatusPlaceholder: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("файл заглушки: %v", err)
	}
	if info.Size() == 0 {
		t.Fatal("пустой файл заглушки")
	}
}
