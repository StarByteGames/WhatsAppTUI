package tui

import (
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"os"
	"os/exec"
	"strings"
	"sync"

	"github.com/nfnt/resize"
)

// imageRenderCache caches rendered terminal output per image path+width so
// repeated View() calls don't re-render or re-exec chafa.
var imageRenderCache sync.Map

// renderImageBlock renders an image for the terminal.  It tries chafa(1)
// first (which uses braille / block characters / sixel depending on the
// terminal and produces much sharper output), then falls back to the
// built-in half-block renderer.
func renderImageBlock(imgPath string, maxCols int) []string {
	cacheKey := fmt.Sprintf("%s:%d", imgPath, maxCols)
	if cached, ok := imageRenderCache.Load(cacheKey); ok {
		return cached.([]string)
	}

	lines := renderImageWithChafa(imgPath, maxCols)
	if lines == nil {
		lines = renderImageHalfBlock(imgPath, maxCols)
	}

	if lines != nil {
		imageRenderCache.Store(cacheKey, lines)
	}
	return lines
}

// renderImageWithChafa shells out to chafa(1) for high-quality terminal
// image rendering.  Returns nil if chafa is not installed.
func renderImageWithChafa(imgPath string, maxCols int) []string {
	cols := maxCols
	rows := cols * 3 / 8 // roughly 3:8 aspect for compact look
	cmd := exec.Command("chafa",
		"--format", "symbols",
		"--symbols", "all",
		"--size", fmt.Sprintf("%dx%d", cols, rows),
		imgPath,
	)
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	result := strings.Split(strings.TrimRight(string(out), "\n\r"), "\n")
	if len(result) == 0 || (len(result) == 1 && result[0] == "") {
		return nil
	}
	return result
}

// renderImageHalfBlock converts an image into ANSI true-color half-block
// characters (▄) as a fallback when chafa is not available.
func renderImageHalfBlock(imgPath string, maxCols int) []string {
	f, err := os.Open(imgPath)
	if err != nil {
		return nil
	}
	defer f.Close()

	img, _, err := image.Decode(f)
	if err != nil {
		return nil
	}

	// Resize to fit the panel width (compact: max 40 cols).
	targetW := maxCols
	if targetW > 40 {
		targetW = 40
	}
	if targetW < 10 {
		targetW = 10
	}
	img = resize.Resize(uint(targetW), 0, img, resize.Lanczos3)

	bounds := img.Bounds()
	w := bounds.Dx()
	h := bounds.Dy()

	var lines []string
	// Process 2 rows of pixels at a time → 1 terminal row.
	for y := bounds.Min.Y; y < bounds.Min.Y+h; y += 2 {
		var sb strings.Builder
		for x := bounds.Min.X; x < bounds.Min.X+w; x++ {
			// Top pixel → background colour.
			rt, gt, bt, _ := img.At(x, y).RGBA()
			tr, tg, tb := rt>>8, gt>>8, bt>>8

			// Bottom pixel → foreground colour (may be out of bounds).
			var br, bg, bb uint32
			if y+1 < bounds.Min.Y+h {
				rb, gb, bb2, _ := img.At(x, y+1).RGBA()
				br, bg, bb = rb>>8, gb>>8, bb2>>8
			} else {
				br, bg, bb = tr, tg, tb
			}

			// \033[48;2;R;G;Bm = set background, \033[38;2;R;G;Bm = set foreground
			fmt.Fprintf(&sb, "\033[48;2;%d;%d;%dm\033[38;2;%d;%d;%dm▄",
				tr, tg, tb, br, bg, bb)
		}
		sb.WriteString("\033[0m") // reset
		lines = append(lines, sb.String())
	}
	return lines
}
