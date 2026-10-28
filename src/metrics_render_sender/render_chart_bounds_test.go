package main

import (
	"image/color"
	"testing"

	"github.com/fogleman/gg"
)

func TestSimpleChartKeepsOutOfRangeValuesInsideItem(t *testing.T) {
	for _, testValue := range []float64{-100, 200} {
		dc := gg.NewContext(100, 100)
		dc.SetRGB(0, 0, 0)
		dc.Clear()
		min, max := 0.0, 100.0
		item := &ItemConfig{Type: itemTypeSimpleChart, X: 20, Y: 40, Width: 60, Height: 20, MinValue: &min, MaxValue: &max}
		item.runtime = renderItemRuntime{prepared: true, historyKey: "chart", historyPoints: 10, simpleChart: renderSimpleChartRuntime{lineWidth: 1}}
		history := newRenderHistoryStore()
		history.append("chart", testValue, 10)
		value := &CollectValue{Value: testValue}
		monitor := &RenderMonitorSnapshot{name: "test", available: true, value: value}
		frame := &RenderFrame{history: history, items: map[*ItemConfig]renderItemState{item: {monitor: monitor}}}
		if err := NewLineChartRenderer().Render(dc, item, frame, nil, &MonitorConfig{}); err != nil {
			t.Fatal(err)
		}
		for y := 0; y < 100; y++ {
			if y >= item.Y && y < item.Y+item.Height {
				continue
			}
			for x := 0; x < 100; x++ {
				if pixel := color.RGBAModel.Convert(dc.Image().At(x, y)).(color.RGBA); pixel != (color.RGBA{A: 255}) {
					t.Fatalf("value %v drew outside item at (%d,%d): %v", testValue, x, y, pixel)
				}
			}
		}
	}
}
