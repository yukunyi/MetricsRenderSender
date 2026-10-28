package main

import (
	"image/color"
	"testing"

	"github.com/fogleman/gg"
)

func TestParseColorPreservesUnassociatedChannels(t *testing.T) {
	for _, input := range []string{"rgba(255, 0, 0, 0.5)", "#ff00007f", "#f007"} {
		t.Run(input, func(t *testing.T) {
			got := color.NRGBAModel.Convert(parseColor(input)).(color.NRGBA)
			wantAlpha := uint8(127)
			if input == "#f007" {
				wantAlpha = 119
			}
			if want := (color.NRGBA{R: 255, A: wantAlpha}); got != want {
				t.Fatalf("parsed color = %v, want %v", got, want)
			}
		})
	}
}

func TestApplyAlphaPreservesColorChannels(t *testing.T) {
	for _, input := range []string{"#ff8040", "#ff804080", "rgba(255, 128, 64, 0.25)"} {
		if got := applyAlpha(input, 0.5); got != "#ff80407f" {
			t.Errorf("applyAlpha(%q, 0.5) = %q, want #ff80407f", input, got)
		}
	}
}

func TestParseColorCompositesTransparency(t *testing.T) {
	for _, input := range []string{"rgba(255, 0, 0, 0.5)", "#ff00007f", "#f007", applyAlpha("#ff000080", 0.5)} {
		t.Run(input, func(t *testing.T) {
			dc := gg.NewContext(4, 4)
			dc.SetRGB(0, 0, 0)
			dc.Clear()
			dc.SetColor(parseColor(input))
			dc.DrawRectangle(0, 0, 4, 4)
			dc.Fill()
			wantRed := uint8(127)
			if input == "#f007" {
				wantRed = 119
			}
			got := color.RGBAModel.Convert(dc.Image().At(2, 2)).(color.RGBA)
			if want := (color.RGBA{R: wantRed, A: 255}); got != want {
				t.Fatalf("composited pixel = %v, want %v", got, want)
			}
		})
	}
}
