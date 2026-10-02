// Command icongen turns the shipped artwork into the desktop icon files.
//
// The master is branding/icon-source.png: a black canvas with the dark rounded
// square and the white Synapass mark. This crops to that square, re-applies
// the rounded corners as a real alpha mask (so the icon reads as a tile on
// light and dark taskbars instead of a black square), and writes:
//
//	assets/icon.png    256px master raster
//	assets/icon.ico    256/48/32/16 PNG payloads in an ICO container
//	cmd/installer/brand.png, dashboard/public/icon.png and
//	dashboard/src/app/favicon.ico for the wizard, the auth screens and the
//	browser tab
//
// The master lives in branding/ rather than assets/ so it is not shipped: the
// installer copies assets/ into every installation.
//
// Everything downstream — the executable icons (rsrc), the tray icon, the
// Start Menu and desktop shortcuts, and the Add/Remove Programs entry — reads
// these two files, so regenerating them re-skins the whole product.
//
// Pure stdlib, so any checkout can rebuild the checked-in assets:
//
//	go run ./cmd/icongen [source.png]
package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"os"
	"path/filepath"
)

// master is the size of the raster written to icon.png and the largest ICO
// payload. 256 is what Windows and the shell ask for at their highest.
const master = 256

// cornerRadius is the rounded-square corner radius as a fraction of the tile's
// width, the same proportion the artwork ships with.
const cornerRadius = 0.2237

// icoSizes are the payload sizes in the ICO. 0 means 256 in the directory
// entry, so it is spelled out here only for clarity.
var icoSizes = []int{256, 48, 32, 16}

func main() {
	srcPath := ""
	if len(os.Args) > 1 {
		srcPath = os.Args[1]
	}

	root, err := repoRoot()
	if err != nil {
		fatal(err)
	}
	if srcPath == "" {
		srcPath = filepath.Join(root, "branding", "icon-source.png")
	}

	art, err := loadArtwork(srcPath)
	if err != nil {
		fatal(err)
	}

	tile := cropToTile(art)
	tile = resize(tile, master, master)
	tile = applyRoundedMask(tile, int(math.Round(float64(master)*cornerRadius)))

	var pngFull bytes.Buffer
	if err := png.Encode(&pngFull, tile); err != nil {
		fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "assets", "icon.png"), pngFull.Bytes(), 0o644); err != nil {
		fatal(err)
	}

	payloads := [][]byte{pngFull.Bytes()}
	for _, s := range icoSizes {
		if s == master {
			continue
		}
		var b bytes.Buffer
		if err := png.Encode(&b, resize(tile, s, s)); err != nil {
			fatal(err)
		}
		payloads = append(payloads, b.Bytes())
	}
	if err := os.WriteFile(filepath.Join(root, "assets", "icon.ico"), buildICO(payloads), 0o644); err != nil {
		fatal(err)
	}
	written := []string{"assets/icon.png", "assets/icon.ico"}

	// Every other surface that shows the mark, so one command re-skins the
	// whole product rather than leaving a surface on the old artwork:
	//
	//	cmd/installer/brand.png     the wizard's brand mark. It is embedded in
	//	                           the installer, which renders before anything is
	//	                           installed, so it cannot read it off disk.
	//	dashboard/public/icon.png   served at /icon.png for the auth screens.
	//	dashboard/src/app/favicon.ico  the dashboard's browser-tab favicon.
	brand := resize(tile, 128, 128)
	if err := writePNG(filepath.Join(root, "cmd", "installer", "brand.png"), brand); err != nil {
		fatal(err)
	}
	written = append(written, "cmd/installer/brand.png")

	dashboard := filepath.Join(root, "..", "dashboard")
	// public/icon.png is served at /icon.png for the auth screens; the app
	// directory's favicon.ico covers the browser tab and is served from the
	// framework route, which the middleware already exempts.
	if err := writePNG(filepath.Join(dashboard, "public", "icon.png"), tile); err != nil {
		fatal(err)
	}
	written = append(written, "dashboard/public/icon.png")

	fav := filepath.Join(dashboard, "src", "app", "favicon.ico")
	if err := os.MkdirAll(filepath.Dir(fav), 0o755); err != nil {
		fatal(err)
	}
	if err := os.WriteFile(fav, buildICO(payloads), 0o644); err != nil {
		fatal(err)
	}
	written = append(written, "dashboard/src/app/favicon.ico")

	for _, w := range written {
		fmt.Println("wrote", w)
	}
}

func writePNG(path string, img image.Image) error {
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		return err
	}
	return os.WriteFile(path, b.Bytes(), 0o644)
}

func loadArtwork(path string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("read artwork: %w", err)
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		return nil, fmt.Errorf("decode %s: %w", path, err)
	}
	return img, nil
}

// cropToTile trims the black canvas down to the rounded square. The artwork is
// delivered on a black field, so the tile is found as the bounding box of every
// pixel that is not pure black; artwork that already fills its canvas is
// returned untouched.
func cropToTile(src image.Image) *image.NRGBA {
	b := src.Bounds()
	minX, minY, maxX, maxY := b.Max.X, b.Max.Y, b.Min.X-1, b.Min.Y-1
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, _ := src.At(x, y).RGBA()
			if r>>8 > 2 || g>>8 > 2 || bl>>8 > 2 {
				if x < minX {
					minX = x
				}
				if x > maxX {
					maxX = x
				}
				if y < minY {
					minY = y
				}
				if y > maxY {
					maxY = y
				}
			}
		}
	}
	if maxX < minX || maxY < minY {
		// Nothing but black: keep the canvas rather than produce an empty icon.
		minX, minY, maxX, maxY = b.Min.X, b.Min.Y, b.Max.X-1, b.Max.Y-1
	}
	out := image.NewNRGBA(image.Rect(0, 0, maxX-minX+1, maxY-minY+1))
	draw.Draw(out, out.Bounds(), src, image.Point{minX, minY}, draw.Src)
	return out
}

// resize scales with a box filter: every destination pixel averages the source
// pixels it covers. A nearest-neighbour reduction of a 1000px master to 256
// would alias the mark's edges badly, which is exactly where an icon is
// scrutinised.
func resize(src *image.NRGBA, w, h int) *image.NRGBA {
	sb := src.Bounds()
	dst := image.NewNRGBA(image.Rect(0, 0, w, h))
	for dy := 0; dy < h; dy++ {
		y0 := sb.Min.Y + dy*sb.Dy()/h
		y1 := sb.Min.Y + (dy+1)*sb.Dy()/h
		if y1 <= y0 {
			y1 = y0 + 1
		}
		for dx := 0; dx < w; dx++ {
			x0 := sb.Min.X + dx*sb.Dx()/w
			x1 := sb.Min.X + (dx+1)*sb.Dx()/w
			if x1 <= x0 {
				x1 = x0 + 1
			}
			var sr, sg, sb_, n uint64
			for y := y0; y < y1 && y < sb.Max.Y; y++ {
				for x := x0; x < x1 && x < sb.Max.X; x++ {
					c := src.NRGBAAt(x, y)
					// Weight colour by alpha so transparent edges do not bleed
					// the black canvas into the mark.
					a := uint64(c.A)
					sr += uint64(c.R) * a
					sg += uint64(c.G) * a
					sb_ += uint64(c.B) * a
					n += a
				}
			}
			if n == 0 {
				continue
			}
			dst.SetNRGBA(dx, dy, color.NRGBA{
				R: uint8(sr / n),
				G: uint8(sg / n),
				B: uint8(sb_ / n),
				A: uint8(n / uint64((x1-x0)*(y1-y0))),
			})
		}
	}
	return dst
}

// applyRoundedMask makes the corners of the tile transparent so the icon sits
// on the taskbar as a tile. The coverage of each corner pixel is estimated
// from a 4x4 grid of samples, which antialiases the arc.
func applyRoundedMask(src *image.NRGBA, radius int) *image.NRGBA {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if radius <= 0 || radius > w/2 {
		return src
	}
	const samples = 4
	inside := func(px, py float64) bool {
		// Distance from the corner arc centres, which sit inset by the radius.
		cx, cy := px, py
		switch {
		case px < float64(radius):
			cx = float64(radius)
		case px > float64(w)-float64(radius):
			cx = float64(w - radius)
		}
		switch {
		case py < float64(radius):
			cy = float64(radius)
		case py > float64(h)-float64(radius):
			cy = float64(h - radius)
		}
		dx, dy := px-cx, py-cy
		return dx*dx+dy*dy <= float64(radius)*float64(radius)
	}
	out := image.NewNRGBA(b)
	step := 1.0 / samples
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			hit := 0
			for sy := 0; sy < samples; sy++ {
				for sx := 0; sx < samples; sx++ {
					if inside(float64(x)+(float64(sx)+0.5)*step, float64(y)+(float64(sy)+0.5)*step) {
						hit++
					}
				}
			}
			c := src.NRGBAAt(b.Min.X+x, b.Min.Y+y)
			c.A = uint8(hit * 255 / (samples * samples))
			out.SetNRGBA(b.Min.X+x, b.Min.Y+y, c)
		}
	}
	return out
}

// buildICO wraps PNG payloads in an ICO container. A zero width or height byte
// means 256, which is how the format encodes the largest payload.
func buildICO(payloads [][]byte) []byte {
	n := len(payloads)
	buf := new(bytes.Buffer)
	_ = binary.Write(buf, binary.LittleEndian, uint16(0))
	_ = binary.Write(buf, binary.LittleEndian, uint16(1))
	_ = binary.Write(buf, binary.LittleEndian, uint16(n))
	offset := uint32(6 + 16*n)
	for _, p := range payloads {
		img, _ := png.Decode(bytes.NewReader(p))
		// A 256px payload must be stored as 0: the directory byte is a single
		// byte and 256 does not fit, and zero is what the format defines as
		// 256. The conversion below produces exactly that.
		wb, hb := byte(img.Bounds().Dx()), byte(img.Bounds().Dy())
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
