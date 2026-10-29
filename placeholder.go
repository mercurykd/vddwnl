package main

import (
	"bytes"
	"compress/flate"
	"encoding/binary"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
)

const (
	statusImageWidth  = 640
	statusImageHeight = 360
)

// writeStatusPlaceholder создаёт картинку-заглушку, которая позже заменяется
// готовым видео через editMessageMedia.
func writeStatusPlaceholder(dir string) (string, error) {
	data, err := statusPlaceholderPNG()
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, ".status.png")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// statusPlaceholderPNG рисует тёмный фон с кнопкой Play и кодирует PNG вручную,
// чтобы не зависеть от пакета image.
func statusPlaceholderPNG() ([]byte, error) {
	const w, h = statusImageWidth, statusImageHeight
	img := make([]byte, w*h*4)
	for i := 0; i < len(img); i += 4 {
		img[i], img[i+1], img[i+2], img[i+3] = 23, 27, 36, 255
	}
	set := func(x, y int, r, g, b byte) {
		i := (y*w + x) * 4
		img[i], img[i+1], img[i+2], img[i+3] = r, g, b, 255
	}

	const cx, cy, radius = w / 2, h / 2, 62
	for y := cy - radius; y <= cy+radius; y++ {
		for x := cx - radius; x <= cx+radius; x++ {
			dx, dy := x-cx, y-cy
			if dx*dx+dy*dy <= radius*radius {
				set(x, y, 47, 129, 247)
			}
		}
	}

	for y := cy - 30; y <= cy+30; y++ {
		width := 44 * (30 - absInt(y-cy)) / 30
		for x := cx - 10; x < cx-10+width; x++ {
			set(x, y, 255, 255, 255)
		}
	}

	return encodePNG(w, h, img)
}

func encodePNG(width, height int, rgba []byte) ([]byte, error) {
	var out bytes.Buffer
	out.Write([]byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a})

	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:], uint32(width))
	binary.BigEndian.PutUint32(ihdr[4:], uint32(height))
	ihdr[8] = 8 // глубина цвета
	ihdr[9] = 6 // RGBA
	writePNGChunk(&out, "IHDR", ihdr)

	row := make([]byte, width*4+1)
	var raw bytes.Buffer
	for y := 0; y < height; y++ {
		row[0] = 0 // filter: none
		copy(row[1:], rgba[y*width*4:(y+1)*width*4])
		raw.Write(row)
	}

	var zlib bytes.Buffer
	// zlib-заголовок: deflate, окно 32 КиБ, без словаря.
	zlib.Write([]byte{0x78, 0x9c})
	fw, err := flate.NewWriter(&zlib, flate.DefaultCompression)
	if err != nil {
		return nil, err
	}
	if _, err := fw.Write(raw.Bytes()); err != nil {
		return nil, err
	}
	if err := fw.Close(); err != nil {
		return nil, err
	}
	var sum [4]byte
	binary.BigEndian.PutUint32(sum[:], adler32sum(raw.Bytes()))
	zlib.Write(sum[:])
	writePNGChunk(&out, "IDAT", zlib.Bytes())
	writePNGChunk(&out, "IEND", nil)
	return out.Bytes(), nil
}

func writePNGChunk(w io.Writer, typ string, data []byte) {
	var buf [4]byte
	binary.BigEndian.PutUint32(buf[:], uint32(len(data)))
	w.Write(buf[:])
	w.Write([]byte(typ))
	w.Write(data)
	crc := crc32.NewIEEE()
	crc.Write([]byte(typ))
	crc.Write(data)
	binary.BigEndian.PutUint32(buf[:], crc.Sum32())
	w.Write(buf[:])
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// adler32sum считает контрольную сумму Adler-32, которой zlib завершает поток.
func adler32sum(data []byte) uint32 {
	const mod = 65521
	var a, b uint32 = 1, 0
	for _, c := range data {
		a = (a + uint32(c)) % mod
		b = (b + a) % mod
	}
	return b<<16 | a
}
