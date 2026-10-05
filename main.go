package main

import (
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"errors"
	"flag"
	"log"
	"net"
	"net/http"
	"os"
	"slices"
	"strconv"

	"github.com/gen2brain/avif"
	"github.com/kovidgoyal/imaging"
)

//go:embed index.html
var index []byte

var formats = []imaging.Format{imaging.PNG, imaging.JPEG, imaging.WEBP}

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
		img, _, err := imaging.DecodeAll(r.Body, imaging.Backends(imaging.GO_IMAGE))
		if err != nil || img == nil || !slices.Contains(formats, img.Metadata.Format) {
			http.Error(w, "unsupported image", http.StatusUnsupportedMediaType)
			return
		}

		id := make([]byte, 16)
		rand.Read(id)
		name := hex.EncodeToString(id) + ".avif"
		tmp := "." + name
		defer root.Remove(tmp)

		f, err := root.Create(tmp)
		if err == nil {
			err = errors.Join(avif.Encode(f, img.SingleFrame(), avif.Options{Quality: 55, Speed: 8}), f.Chmod(0o644), f.Sync(), f.Close())
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
