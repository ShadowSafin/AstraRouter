// Command icongen renders the AstraRouter desktop icon: a violet spark on a
// dark gradient tile. It writes assets/icon.png and assets/icon.ico (the ICO
// wraps the PNG payloads, which Windows accepts). Pure stdlib, so any
// checkout can regenerate the checked-in assets.
package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
)

const size = 256

func lerp(a, b uint8, t float64) uint8 {
	return uint8(float64(a)*(1-t) + float64(b)*t)
}

func main() {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	top := color.NRGBA{0x1e, 0x1b, 0x4b, 0xff}    // violet-950
	bottom := color.NRGBA{0x0b, 0x12, 0x20, 0xff} // dashboard bg
	for y := 0; y < size; y++ {
		t := float64(y) / float64(size-1)
		for x := 0; x < size; x++ {
			img.Set(x, y, color.NRGBA{
				R: lerp(top.R, bottom.R, t),
				G: lerp(top.G, bottom.G, t),
				B: lerp(top.B, bottom.B, t),
				A: 0xff,
			})
		}
	}
	cx, cy := float64(size)/2, float64(size)/2
	// Glow: soft violet disc behind the spark.
	glow := color.NRGBA{0x8b, 0x5c, 0xf6, 0x55}
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			dx, dy := float64(x)-cx, float64(y)-cy
			d := math.Sqrt(dx*dx + dy*dy)
			if d < 110 {
				a := uint8(90 * (1 - d/110))
				cur := img.NRGBAAt(x, y)
				img.Set(x, y, over(cur, color.NRGBA{glow.R, glow.G, glow.B, a}))
			}
		}
	}
	// Spark: a four-point star (long vertical, shorter horizontal).
	star := color.NRGBA{0xff, 0xff, 0xff, 0xff}
	drawDiamond(img, cx, cy, 30, 96, star)
	drawDiamond(img, cx, cy, 84, 26, star)
	// Core dot in bright violet.
	dot := color.NRGBA{0xa7, 0x8b, 0xfa, 0xff}
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			dx, dy := float64(x)-cx, float64(y)-cy
			if dx*dx+dy*dy < 14*14 {
				img.Set(x, y, dot)
			}
		}
	}

	root, err := repoRoot()
	if err != nil {
		fatal(err)
	}
	pngPath := filepath.Join(root, "assets", "icon.png")
	icoPath := filepath.Join(root, "assets", "icon.ico")

	var pngFull bytes.Buffer
	if err := png.Encode(&pngFull, img); err != nil {
		fatal(err)
	}
	if err := os.WriteFile(pngPath, pngFull.Bytes(), 0o644); err != nil {
		fatal(err)
	}
	payloads := [][]byte{pngFull.Bytes()}
	for _, s := range []int{48, 32, 16} {
		var b bytes.Buffer
		if err := png.Encode(&b, downscale(img, s)); err != nil {
			fatal(err)
		}
		payloads = append(payloads, b.Bytes())
	}
	if err := os.WriteFile(icoPath, buildICO(payloads), 0o644); err != nil {
		fatal(err)
	}
	fmt.Printf("wrote %s and %s\n", pngPath, icoPath)
}

// drawDiamond fills a rhombus centered at (cx,cy) with horizontal radius rx
// and vertical radius ry.
func drawDiamond(img *image.NRGBA, cx, cy, rx, ry float64, c color.NRGBA) {
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			nx := math.Abs(float64(x)-cx) / rx
			ny := math.Abs(float64(y)-cy) / ry
			if nx+ny <= 1 {
				img.Set(x, y, c)
			}
		}
	}
}

func over(dst, src color.NRGBA) color.NRGBA {
	a := float64(src.A) / 255
	return color.NRGBA{
		R: uint8(float64(src.R)*a + float64(dst.R)*(1-a)),
		G: uint8(float64(src.G)*a + float64(dst.G)*(1-a)),
		B: uint8(float64(src.B)*a + float64(dst.B)*(1-a)),
		A: 0xff,
	}
}

func downscale(src *image.NRGBA, s int) *image.NRGBA {
	dst := image.NewNRGBA(image.Rect(0, 0, s, s))
	sb := src.Bounds()
	sw, sh := sb.Dx(), sb.Dy()
	for y := 0; y < s; y++ {
		for x := 0; x < s; x++ {
			dst.Set(x, y, src.NRGBAAt(sb.Min.X+x*sw/s, sb.Min.Y+y*sh/s))
		}
	}
	return dst
}

// buildICO wraps PNG payloads in an ICO container. A zero byte means 256.
func buildICO(payloads [][]byte) []byte {
	n := len(payloads)
	buf := new(bytes.Buffer)
	_ = binary.Write(buf, binary.LittleEndian, uint16(0))
	_ = binary.Write(buf, binary.LittleEndian, uint16(1))
	_ = binary.Write(buf, binary.LittleEndian, uint16(n))
	offset := uint32(6 + 16*n)
	for _, p := range payloads {
		img, _ := png.Decode(bytes.NewReader(p))
		w, h := img.Bounds().Dx(), img.Bounds().Dy()
		wb, hb := byte(w), byte(h)
		if w >= 256 {
			wb = 0
		}
		if h >= 256 {
			hb = 0
		}
		buf.Write([]byte{wb, hb, 0, 0})
		_ = binary.Write(buf, binary.LittleEndian, uint16(1))
		_ = binary.Write(buf, binary.LittleEndian, uint16(32))
		_ = binary.Write(buf, binary.LittleEndian, uint32(len(p)))
		_ = binary.Write(buf, binary.LittleEndian, offset)
		offset += uint32(len(p))
	}
	for _, p := range payloads {
		buf.Write(p)
	}
	return buf.Bytes()
}

func repoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no go.mod found")
		}
		dir = parent
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "icongen:", err)
	os.Exit(1)
}
