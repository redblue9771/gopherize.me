package server

import (
	"bytes"
	"image"
	"image/draw"
	"image/png"
	"net/http"
	"strings"
	"sync"

	"cloud.google.com/go/storage"
	"github.com/pkg/errors"
	"golang.org/x/net/context"
	"google.golang.org/appengine"
	"google.golang.org/appengine/file"
	"google.golang.org/appengine/log"
	"google.golang.org/appengine/memcache"
)

type stack []image.Image

func (s server) renderHandler(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	imagesStr := q.Get("images")
	images := strings.Split(imagesStr, "|")
	if len(images) == 0 {
		http.Error(w, "Must specify at least one image", http.StatusBadRequest)
		return
	}
	ctx := appengine.NewContext(r)

	cacheItem, err := memcache.Get(ctx, imagesStr)
	if err == nil {
		// exit early - from cache
		log.Debugf(ctx, "cache hit: %s", imagesStr)
		s.respondWithPng(ctx, w, r, cacheItem.Value)
		return
	}

	bucket, err := file.DefaultBucketName(ctx)
	if err != nil {
		s.responderr(ctx, w, r, http.StatusInternalServerError, err)
		return
	}
	client, err := storage.NewClient(ctx)
	if err != nil {
		s.responderr(ctx, w, r, http.StatusInternalServerError, err)
		return
	}
	imgObjects := s.loadimages(ctx, client.Bucket(bucket), images...)
	var first image.Image
	for _, img := range imgObjects {
		if img == nil {
			continue
		}
		first = img
		break
	}
	if first == nil {
		s.responderr(ctx, w, r, http.StatusInternalServerError, errors.Wrap(err, "Artwork is being updated - please try again later"))
		return
	}
	output := image.NewRGBA(first.Bounds())
	for _, img := range imgObjects {
		if img == nil {
			// skip missing images
			continue
		}
		draw.Draw(output, output.Bounds(), img, image.ZP, draw.Over)
	}
	// encode into a buffer
	var buf bytes.Buffer
	if err := png.Encode(&buf, output); err != nil {
		log.Errorf(ctx, "PNG encode: %s", err)
		return
	}
	// write buffer as response
	s.respondWithPng(ctx, w, r, buf.Bytes())
	// put result in cache
	cacheItem = &memcache.Item{
		Key:   imagesStr,
		Value: buf.Bytes(),
	}
	if err := memcache.Set(ctx, cacheItem); err != nil {
		log.Warningf(ctx, "memcache set: %s", err)
	}
}

func (s server) respondWithPng(ctx context.Context, w http.ResponseWriter, r *http.Request, data []byte) {
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Content-Disposition", "attachment; filename=gopherizeme.png;")
	if _, err := w.Write(data); err != nil {
		log.Warningf(ctx, "write png: %s", err)
	}
}

func (s server) loadimages(ctx context.Context, bucket *storage.BucketHandle, names ...string) []image.Image {
	var wg sync.WaitGroup
	var l sync.Mutex
	images := make(map[string]image.Image)
	errs := make(map[string]error)
	for _, name := range names {
		if len(name) == 0 {
			continue
		}
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			r, err := bucket.Object(name).NewReader(ctx)
			if err != nil {
				l.Lock()
				errs[name] = err
				images[name] = nil
				l.Unlock()
				return
			}
			imageObj, _, err := image.Decode(r)
			if err != nil {
				l.Lock()
				errs[name] = err
				images[name] = nil
				l.Unlock()
				return
			}
			l.Lock()
			images[name] = imageObj
			l.Unlock()
		}(name)
	}
	wg.Wait()
	if len(errs) > 0 {
		log.Warningf(ctx, "processing images: %s", errs)
	}
	imagesList := make([]image.Image, len(names))
	for i, name := range names {
		imagesList[i] = images[name]
	}
	return imagesList
}
