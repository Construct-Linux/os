package qemu

import (
	"bufio"
	"fmt"
	"image"
	"image/png"
	"io"
	"os"
	"strconv"
)

// PPMToPNG converts QEMU's screendump to PNG.
func PPMToPNG(src, dst string) error {
	img, err := DecodePPM(src)
	if err != nil {
		return err
	}
	o, err := os.Create(dst)
	if err != nil {
		return err
	}
	if err := png.Encode(o, img); err != nil {
		o.Close()
		return err
	}
	return o.Close()
}

// DecodePPM reads QEMU's screendump (binary P6, 8-bit).
func DecodePPM(p string) (*image.RGBA, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	br := bufio.NewReader(f)
	var fields []int
	magic := ""
	for len(fields) < 3 {
		tok, err := ppmToken(br)
		if err != nil {
			return nil, err
		}
		if magic == "" {
			magic = tok
			continue
		}
		n, err := strconv.Atoi(tok)
		if err != nil {
			return nil, err
		}
		fields = append(fields, n)
	}
	if magic != "P6" || fields[2] != 255 {
		return nil, fmt.Errorf("%s: not an 8-bit P6", p)
	}
	w, h := fields[0], fields[1]
	pix := make([]byte, w*h*3)
	if _, err := io.ReadFull(br, pix); err != nil {
		return nil, err
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := range w * h {
		copy(img.Pix[4*i:4*i+3], pix[3*i:3*i+3])
		img.Pix[4*i+3] = 255
	}
	return img, nil
}

// ppmToken reads one whitespace-separated header token, and the single whitespace after it.
func ppmToken(br *bufio.Reader) (string, error) {
	var tok []byte
	for {
		c, err := br.ReadByte()
		if err != nil {
			return "", err
		}
		if c == '#' {
			_, _ = br.ReadString('\n')
			continue
		}
		if c == ' ' || c == '\n' || c == '\t' || c == '\r' {
			if len(tok) > 0 {
				return string(tok), nil
			}
			continue
		}
		tok = append(tok, c)
	}
}
