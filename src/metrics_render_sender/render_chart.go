package main

import (
	"math"

	"github.com/fogleman/gg"
)

type LineChartRenderer struct{}

func NewLineChartRenderer() *LineChartRenderer {
	return &LineChartRenderer{}
}

func (c *LineChartRenderer) GetType() string {
	return itemTypeSimpleChart
}

func (c *LineChartRenderer) Render(dc *gg.Context, item *ItemConfig, frame *RenderFrame, fontCache *FontCache, config *MonitorConfig) error {
	monitor, value, ok := frame.AvailableItemValue(item)
	if !ok {
		return nil
	}
	val, ok := tryGetFloat64(value.Value)
	if !ok {
		return nil
	}

	history := appendFrameRenderHistory(frame, item, val)

	radius := resolveItemRadius(item, config, 0)
	drawRoundedBackground(dc, item.X, item.Y, item.Width, item.Height, resolveItemBackground(item, config), radius)

	minVal, maxVal := resolveEffectiveMinMax(item, value, history, val)

	lineColor := resolveMonitorColor(item, monitor, config)
	lineWidth := item.runtime.simpleChart.lineWidth
	enableThresholdColors := item.runtime.simpleChart.enableThresholdColors
	if !item.runtime.prepared {
		lineWidth = clampRenderFloat(getItemAttrFloatCfg(item, config, "line_width", 1.5), 1)
		enableThresholdColors = getItemAttrBoolCfg(item, config, "enable_threshold_colors", false)
	}
	padding := 2.0
	chartX := float64(item.X) + padding
	chartY := float64(item.Y) + padding
	chartWidth := float64(item.Width) - 2*padding
	chartHeight := float64(item.Height) - 2*padding
	if chartWidth <= 1 || chartHeight <= 1 {
		drawBaseItemBorder(dc, item, config, radius)
		return nil
	}

	type chartPoint struct {
		x float64
		y float64
		v float64
	}
	pointsOnChart := make([]chartPoint, 0, len(history))
	for idx, histValue := range history {
		if !isFiniteHistoryValue(histValue) {
			continue
		}
		x := chartX
		if len(history) > 1 {
			x = chartX + float64(idx)*chartWidth/float64(len(history)-1)
		}
		y := chartY + chartHeight - (histValue-minVal)/(maxVal-minVal)*chartHeight
		y = clampFloat64(y, chartY, chartY+chartHeight)
		pointsOnChart = append(pointsOnChart, chartPoint{x: x, y: y, v: histValue})
	}
	if len(pointsOnChart) < 2 {
		drawBaseItemBorder(dc, item, config, radius)
		return nil
	}

	if enableThresholdColors {
		dc.SetLineWidth(lineWidth)
		for idx := 1; idx < len(pointsOnChart); idx++ {
			p0 := pointsOnChart[idx-1]
			p1 := pointsOnChart[idx]
			segmentColor := resolveMonitorValueColor(item, monitor.name, value, (p0.v+p1.v)/2, config)
			dc.SetColor(parseColor(segmentColor))
			dc.DrawLine(p0.x, p0.y, p1.x, p1.y)
			dc.Stroke()
		}
	} else {
		dc.MoveTo(pointsOnChart[0].x, pointsOnChart[0].y)
		for idx := 1; idx < len(pointsOnChart); idx++ {
			p := pointsOnChart[idx]
			dc.LineTo(p.x, p.y)
		}
		dc.SetColor(parseColor(lineColor))
		dc.SetLineWidth(lineWidth)
		dc.Stroke()
	}

	drawBaseItemBorder(dc, item, config, radius)
	_ = fontCache
	return nil
}

func isFiniteHistoryValue(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}
