package main

import (
	"bytes"
	"compress/zlib"
	"crypto/rand"
	_ "embed"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"flag"
	"image"
	"image/draw"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"log"
	"math"
	"net"
	"net/http"
	"os"
	"slices"
	"strconv"

	"github.com/gen2brain/avif"
	_ "golang.org/x/image/webp"
)

//go:embed index.html
var index []byte

var formats = []string{"png", "jpeg", "webp"}

var xyzD50ToLinearSRGB = [3][3]float64{
	{3.1341122, -1.6173925, -0.4906334},
	{-0.9787873, 1.9162796, 0.0334547},
	{0.0719830, -0.2289859, 1.4053851},
}

var linearToSRGB = func() (lut [1 << 16]uint8) {
	for i := range lut {
		v := float64(i) / 0xFFFF
		if v <= 0.0031308 {
			v *= 12.92
		} else {
			v = 1.055*math.Pow(v, 1/2.4) - 0.055
		}
		lut[i] = uint8(math.Round(v * 255))
	}
	return
}()

func main() {
	dir := flag.String("dir", "", "storage directory")
	listen := flag.String("listen", "127.0.0.1:3000", "listen address, ignored under systemd socket activation")
	flag.Parse()

	root, err := os.OpenRoot(*dir)
	if err != nil {
		log.Fatal(err)
	}

	var ln net.Listener
	if os.Getenv("LISTEN_PID") == strconv.Itoa(os.Getpid()) && os.Getenv("LISTEN_FDS") == "1" {
		ln, err = net.FileListener(os.NewFile(3, "systemd"))
	} else {
		ln, err = net.Listen("tcp", *listen)
	}
	if err != nil {
		log.Fatal(err)
	}

	http.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) { w.Write(index) })

	http.HandleFunc("POST /{$}", func(w http.ResponseWriter, r *http.Request) {
		data, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		img, format, err := image.Decode(bytes.NewReader(data))
		if err != nil || !slices.Contains(formats, format) {
			http.Error(w, "unsupported image", http.StatusUnsupportedMediaType)
			return
		}
		exif, icc := metadata(format, data)
		out := orient(toRGBA(img, iccToSRGB(icc)), orientation(exif))

		id := make([]byte, 16)
		rand.Read(id)
		name := hex.EncodeToString(id) + ".avif"
		tmp := "." + name
		defer root.Remove(tmp)

		f, err := root.Create(tmp)
		if err == nil {
			err = errors.Join(avif.Encode(f, out, avif.Options{Quality: 55, Speed: 8}), f.Chmod(0o644), f.Sync(), f.Close())
		}
		if err == nil {
			err = root.Rename(tmp, name)
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		url := "/" + name
		if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
			url = proto + "://" + r.Host + url
		}
		w.Write([]byte(url))
	})

	log.Fatal(http.Serve(ln, nil))
}

func metadata(format string, data []byte) (exif, icc []byte) {
	be := binary.BigEndian
	payload := func(from, n int) []byte { return data[from:min(from+max(n, 0), len(data))] }
	switch format {
	case "jpeg":
		for i := 2; i+4 <= len(data) && data[i] == 0xFF && data[i+1] != 0xDA; {
			n := int(be.Uint16(data[i+2:]))
			segment := payload(i+4, n-2)
			switch {
			case data[i+1] == 0xE1 && bytes.HasPrefix(segment, []byte("Exif\x00\x00")):
				exif = segment[6:]
			case data[i+1] == 0xE2 && bytes.HasPrefix(segment, []byte("ICC_PROFILE\x00")) && len(segment) > 14:
				icc = append(icc, segment[14:]...)
			}
			i += 2 + n
		}
	case "png":
		for i := 8; i+8 <= len(data); {
			n := int(be.Uint32(data[i:]))
			chunk := payload(i+8, n)
			switch string(data[i+4 : i+8]) {
			case "eXIf":
				exif = chunk
			case "iCCP":
				if k := bytes.IndexByte(chunk, 0); k >= 0 && k+2 <= len(chunk) {
					if z, err := zlib.NewReader(bytes.NewReader(chunk[k+2:])); err == nil {
						icc, _ = io.ReadAll(z)
					}
				}
			}
			i += 12 + n
		}
	case "webp":
		for i := 12; i+8 <= len(data); {
			n := int(binary.LittleEndian.Uint32(data[i+4:]))
			chunk := payload(i+8, n)
			switch string(data[i : i+4]) {
			case "EXIF":
				exif = bytes.TrimPrefix(chunk, []byte("Exif\x00\x00"))
			case "ICCP":
				icc = chunk
			}
			i += 8 + n + n%2
		}
	}
	return
}

func orientation(exif []byte) int {
	if len(exif) < 8 {
		return 1
	}
	var order binary.ByteOrder = binary.BigEndian
	if exif[0] == 'I' {
		order = binary.LittleEndian
	}
	ifd := int(order.Uint32(exif[4:]))
	if ifd+2 > len(exif) {
		return 1
	}
	entries := exif[ifd+2:]
	for i := range min(int(order.Uint16(exif[ifd:])), len(entries)/12) {
		if entry := entries[12*i:]; order.Uint16(entry) == 0x0112 {
			return int(order.Uint16(entry[8:]))
		}
	}
	return 1
}

func iccToSRGB(icc []byte) func(pixel []uint8) {
	if len(icc) < 132 || string(icc[16:24]) != "RGB XYZ " {
		return nil
	}
	be := binary.BigEndian
	tags := map[string][]byte{}
	for i := range min(int(be.Uint32(icc[128:])), (len(icc)-132)/12) {
		entry := icc[132+12*i:]
		if offset, size := int(be.Uint32(entry[4:])), int(be.Uint32(entry[8:])); offset+size <= len(icc) {
			tags[string(entry[:4])] = icc[offset : offset+size]
		}
	}

	var linear [3][256]float64
	var matrix [3][3]float64
	for c, channel := range []string{"r", "g", "b"} {
		curve, xyz := toneCurve(tags[channel+"TRC"]), tags[channel+"XYZ"]
		if curve == nil || len(xyz) < 20 {
			return nil
		}
		for i := range linear[c] {
			linear[c][i] = curve(float64(i) / 255)
		}
		for row := range 3 {
			for k := range 3 {
				matrix[row][c] += xyzD50ToLinearSRGB[row][k] * s15Fixed16(xyz[8+4*k:])
			}
		}
	}

	return func(p []uint8) {
		alpha := uint32(p[3])
		if alpha == 0 {
			return
		}
		var in [3]float64
		for c := range 3 {
			in[c] = linear[c][uint32(p[c])*255/alpha]
		}
		for c, row := range &matrix {
			v := row[0]*in[0] + row[1]*in[1] + row[2]*in[2]
			if !(v > 0) {
				v = 0
			}
			p[c] = uint8((uint32(linearToSRGB[int(min(v, 1)*0xFFFF+0.5)])*alpha + 127) / 255)
		}
	}
}

func toneCurve(tag []byte) func(float64) float64 {
	if len(tag) < 12 {
		return nil
	}
	be := binary.BigEndian
	switch string(tag[:4]) {
	case "curv":
		n := int(be.Uint32(tag[8:]))
		switch {
		case len(tag) < 12+2*n:
			return nil
		case n == 0:
			return func(x float64) float64 { return x }
		case n == 1:
			gamma := float64(be.Uint16(tag[12:])) / 256
			return func(x float64) float64 { return math.Pow(x, gamma) }
		}
		return func(x float64) float64 {
			pos := x * float64(n-1)
			i := min(int(pos), n-2)
			lo, hi := float64(be.Uint16(tag[12+2*i:])), float64(be.Uint16(tag[14+2*i:]))
			return (lo + (hi-lo)*(pos-float64(i))) / 0xFFFF
		}
	case "para":
		kind, counts := int(be.Uint16(tag[8:])), []int{1, 3, 4, 5, 7}
		if kind >= len(counts) || len(tag) < 12+4*counts[kind] {
			return nil
		}
		var p [7]float64
		for i := range counts[kind] {
			p[i] = s15Fixed16(tag[12+4*i:])
		}
		g, a, b, c, d, e, f := p[0], p[1], p[2], p[3], p[4], p[5], p[6]
		switch kind {
		case 0:
			a = 1
		case 1:
			d = -b / a
		case 2:
			d, e, f, c = -b/a, c, c, 0
		}
		return func(x float64) float64 {
			if x >= d {
				return math.Pow(max(a*x+b, 0), g) + e
			}
			return c*x + f
		}
	}
	return nil
}

func s15Fixed16(b []byte) float64 {
	return float64(int32(binary.BigEndian.Uint32(b))) / 65536
}

func toRGBA(img image.Image, toSRGB func([]uint8)) *image.RGBA {
	b := img.Bounds()
	rgba := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(rgba, rgba.Rect, img, b.Min, draw.Src)
	if toSRGB != nil {
		for pixel := range slices.Chunk(rgba.Pix, 4) {
			toSRGB(pixel)
		}
	}
	return rgba
}

func orient(src *image.RGBA, orientation int) *image.RGBA {
	if orientation < 2 || orientation > 8 {
		return src
	}
	w, h := src.Rect.Dx(), src.Rect.Dy()
	transpose := orientation >= 5
	flipX := slices.Contains([]int{2, 3, 7, 8}, orientation)
	flipY := slices.Contains([]int{3, 4, 6, 7}, orientation)
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	if transpose {
		dst = image.NewRGBA(image.Rect(0, 0, h, w))
	}
	for y := range dst.Rect.Dy() {
		for x := range dst.Rect.Dx() {
			sx, sy := x, y
			if transpose {
				sx, sy = y, x
			}
			if flipX {
				sx = w - 1 - sx
			}
			if flipY {
				sy = h - 1 - sy
			}
			dst.SetRGBA(x, y, src.RGBAAt(sx, sy))
		}
	}
	return dst
}
