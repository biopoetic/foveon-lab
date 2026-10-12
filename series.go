package main

import (
	"image"
	"image/color"
	"image/draw"
	"path/filepath"

	"github.com/biopoetic/foveon-lab/preset"
	"github.com/biopoetic/foveon-lab/render"
)

const maxSeries = 6

// seriesPhotos picks up to maxSeries photos from ph's folder for the AI's
// series mode: ph itself plus others spread evenly through the folder.
func (s *server) seriesPhotos(ph *Photo) []*Photo {
	var same []*Photo
	for _, p := range s.photoList() {
		if p != ph && p.Unsupported == "" && filepath.Dir(p.path) == filepath.Dir(ph.path) {
			same = append(same, p)
		}
	}
	out := []*Photo{ph}
	want := min(maxSeries-1, len(same))
	for i := 0; i < want; i++ {
		out = append(out, same[i*len(same)/want])
	}
	return out
}

// renderSheet renders every photo with p into one contact sheet at most
// width wide (2 or 3 columns, each photo letterboxed in its cell).
func (s *server) renderSheet(phs []*Photo, p preset.Params, comp bool, width int) (*image.RGBA, error) {
	cols := 2
	if len(phs) > 4 {
		cols = 3
	}
	rows := (len(phs) + cols - 1) / cols
	const gap = 4
	cellW := (width - gap*(cols-1)) / cols
	cellH := cellW * 2 / 3
	sheet := image.NewRGBA(image.Rect(0, 0, cols*cellW+gap*(cols-1), rows*cellH+gap*(rows-1)))
	draw.Draw(sheet, sheet.Bounds(), &image.Uniform{color.RGBA{24, 24, 24, 255}}, image.Point{}, draw.Src)
	for i, ph := range phs {
		s.sem <- struct{}{}
		b, err := s.base(ph, cellW)
		var img *image.RGBA
		if err == nil {
			img = render.Render(b, p, s.compensation(ph, comp))
		}
		<-s.sem
		if err != nil {
			return nil, err
		}
		// Fit inside the cell (portrait shots are taller than 3:2).
		ib := img.Bounds()
		w, h := ib.Dx(), ib.Dy()
		if h > cellH {
			w, h = w*cellH/h, cellH
		}
		x0 := (i%cols)*(cellW+gap) + (cellW-w)/2
		y0 := (i/cols)*(cellH+gap) + (cellH-h)/2
		dst := image.Rect(x0, y0, x0+w, y0+h)
		if w == ib.Dx() {
			draw.Draw(sheet, dst, img, ib.Min, draw.Src)
		} else {
			scaleNearest(sheet, dst, img)
		}
	}
	return sheet, nil
}

func scaleNearest(dst *image.RGBA, r image.Rectangle, src *image.RGBA) {
	sb := src.Bounds()
	for y := r.Min.Y; y < r.Max.Y; y++ {
		sy := sb.Min.Y + (y-r.Min.Y)*sb.Dy()/r.Dy()
		for x := r.Min.X; x < r.Max.X; x++ {
			sx := sb.Min.X + (x-r.Min.X)*sb.Dx()/r.Dx()
			si, di := src.PixOffset(sx, sy), dst.PixOffset(x, y)
			copy(dst.Pix[di:di+4], src.Pix[si:si+4])
		}
	}
}
