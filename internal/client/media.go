package client

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"image"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"os"
	"path/filepath"

	"github.com/nfnt/resize"
	"go.mau.fi/whatsmeow/proto/waE2E"

	"DevStarByte/internal/db"
	"DevStarByte/internal/state"
)

// downloadSlots limits concurrent media downloads (history syncs can contain
// hundreds of images).
var downloadSlots = make(chan struct{}, 4)

// queueImageDownload downloads and caches an image in the background and
// attaches it to the message when done. Already cached images are attached
// immediately without a download.
func queueImageDownload(s *state.AppState, chatKey, msgID string, img *waE2E.ImageMessage) {
	if s.Client == nil || img == nil {
		return
	}
	if fpath := cachePathFor(img.GetFileSHA256()); fpath != "" {
		if _, err := os.Stat(fpath); err == nil {
			s.SetImagePath(chatKey, msgID, fpath)
			return
		}
	}
	go func() {
		downloadSlots <- struct{}{}
		defer func() { <-downloadSlots }()
		if fpath := downloadAndCacheImage(s, img); fpath != "" {
			s.SetImagePath(chatKey, msgID, fpath)
			s.Notify()
		}
	}()
}

// cachePathFor returns the cache file for a content hash, or "" if the hash is empty.
func cachePathFor(hash []byte) string {
	if len(hash) == 0 {
		return ""
	}
	return filepath.Join(db.MediaCacheDir, hex.EncodeToString(hash[:min(len(hash), 8)])+".jpg")
}

// downloadAndCacheImage downloads an image, shrinks it and stores it as JPEG
// in the media cache. Returns the file path on success, or "" on failure.
func downloadAndCacheImage(s *state.AppState, imgMsg *waE2E.ImageMessage) string {
	data, err := s.Client.Download(context.Background(), imgMsg)
	if err != nil {
		s.Logger.Warning("Failed to download image: " + err.Error())
		return ""
	}
	hash := imgMsg.GetFileSHA256()
	if len(hash) == 0 {
		sum := sha256.Sum256(data)
		hash = sum[:]
	}
	fpath := cachePathFor(hash)
	if _, err := os.Stat(fpath); err == nil {
		return fpath
	}

	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		s.Logger.Warning("Failed to decode image: " + err.Error())
		return ""
	}
	// Max ~40 columns wide (~320px at a typical 8px cell width).
	const maxW = 320
	if img.Bounds().Dx() > maxW {
		img = resize.Resize(maxW, 0, img, resize.Lanczos3)
	}

	// Write to a temp file first so a crash never leaves a truncated image.
	tmp, err := os.CreateTemp(db.MediaCacheDir, "dl-*.tmp")
	if err != nil {
		s.Logger.Warning("Failed to create cache file: " + err.Error())
		return ""
	}
	encErr := jpeg.Encode(tmp, img, &jpeg.Options{Quality: 85})
	closeErr := tmp.Close()
	if encErr != nil || closeErr != nil {
		s.Logger.Warning("Failed to write cached image")
		os.Remove(tmp.Name())
		return ""
	}
	if err := os.Rename(tmp.Name(), fpath); err != nil {
		s.Logger.Warning("Failed to store cached image: " + err.Error())
		os.Remove(tmp.Name())
		return ""
	}
	s.Logger.Info("Cached image: " + fpath)
	return fpath
}
