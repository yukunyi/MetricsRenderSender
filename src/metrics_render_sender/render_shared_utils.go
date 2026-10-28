package main

import (
	"fmt"
	"image/color"
	"math"
	"strings"
	"sync"

	"github.com/fogleman/gg"
	"golang.org/x/image/font"
)

type renderHistoryStore struct {
	mu      sync.Mutex
	history map[string]*renderHistorySeries
}

type renderHistorySeries struct {
	values []float64
	next   int
	size   int
}

func newRenderHistoryStore() *renderHistoryStore {
	return &renderHistoryStore{
		history: make(map[string]*renderHistorySeries),
	}
}

func (s *renderHistoryStore) append(key string, value float64, maxLen int) []float64 {
	if maxLen < 10 {
		maxLen = 10
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	series := s.history[key]
	if series == nil || len(series.values) != maxLen {
		series = resizeRenderHistorySeries(series, maxLen)
		s.history[key] = series
	}
	series.append(value)
	return series.snapshot()
}

func newRenderHistorySeries(size int) *renderHistorySeries {
	if size < 1 {
		size = 1
	}
	return &renderHistorySeries{
		values: make([]float64, size),
	}
}

func resizeRenderHistorySeries(current *renderHistorySeries, size int) *renderHistorySeries {
	resized := newRenderHistorySeries(size)
	if current == nil || len(current.values) == 0 || current.size == 0 {
		return resized
	}
	copyCount := current.size
	if copyCount > len(resized.values) {
		copyCount = len(resized.values)
	}
	start := current.next - copyCount
	for start < 0 {
		start += len(current.values)
	}
	for idx := 0; idx < copyCount; idx++ {
		sourceIndex := start + idx
		if sourceIndex >= len(current.values) {
			sourceIndex -= len(current.values)
		}
		resized.append(current.values[sourceIndex])
	}
	return resized
}

func (s *renderHistorySeries) append(value float64) {
	if s == nil || len(s.values) == 0 {
		return
	}
	s.values[s.next] = value
	s.next++
	if s.next >= len(s.values) {
		s.next = 0
	}
	if s.size < len(s.values) {
		s.size++
	}
}

func (s *renderHistorySeries) snapshot() []float64 {
	if s == nil || len(s.values) == 0 {
		return nil
	}
	current := make([]float64, len(s.values))
	prefix := len(s.values) - s.size
	for idx := 0; idx < prefix; idx++ {
		current[idx] = math.NaN()
	}
	if s.size == 0 {
		return current
	}
	start := s.next - s.size
	for start < 0 {
		start += len(s.values)
	}
	for idx := 0; idx < s.size; idx++ {
		sourceIndex := start + idx
		if sourceIndex >= len(s.values) {
			sourceIndex -= len(s.values)
		}
		current[prefix+idx] = s.values[sourceIndex]
	}
	return current
}

type fullRect struct {
	x float64
	y float64
	w float64
	h float64
}

func defaultRenderHistoryPoints(itemType string) int {
	switch itemType {
	case itemTypeSimpleChart:
		return 60
	case itemTypeFullChart:
		return 90
	case itemTypeSimpleProgress, itemTypeFullProgressH, itemTypeFullProgressV, itemTypeFullGauge:
		return 60
	default:
		return 0
	}
}

func buildRenderHistoryKey(item *ItemConfig) string {
	if item == nil {
		return ""
	}
	return item.Type + "|id:" + item.ID + "|" + item.Monitor
}

func prepareRenderItemRuntime(config *MonitorConfig, item *ItemConfig) {
	if item == nil {
		return
	}
	item.runtime = renderItemRuntime{}
	item.runtime.background = resolveItemBackground(item, config)
	item.runtime.staticColor = resolveItemStaticColor(item, config)
	item.runtime.explicitStaticColor = strings.TrimSpace(resolveStyleOverrideColor(item, config, "color"))
	item.runtime.explicitUnitColor = strings.TrimSpace(resolveStyleOverrideColor(item, config, "unit_color"))
	item.runtime.borderWidth = resolveItemBorderWidth(item, config)
	item.runtime.borderColor = resolveItemBorderColor(item, config)
	item.runtime.radius = resolveItemRadius(item, config, 0)
	if cardRadius, ok := getItemAttrFloatCfgOK(item, config, "card_radius"); ok {
		item.runtime.hasCardRadius = true
		if cardRadius < 0 {
			cardRadius = item.runtime.radius
		}
		item.runtime.cardRadius = cardRadius
	}
	if paddingX, ok := getItemAttrFloatCfgOK(item, config, "content_padding_x"); ok {
		item.runtime.hasPaddingX = true
		item.runtime.paddingX = paddingX
	}
	if paddingY, ok := getItemAttrFloatCfgOK(item, config, "content_padding_y"); ok {
		item.runtime.hasPaddingY = true
		item.runtime.paddingY = paddingY
	}
	item.runtime.valueFontSize = resolveValueFontSize(item, config, 18)
	item.runtime.textFontSize = resolveTextFontSize(item, config, 16)
	item.runtime.unitFontSize = resolveUnitFontSize(item, config, 14)
	item.runtime.titleText = strings.TrimSpace(getItemAttrStringCfg(item, config, "title", ""))
	item.runtime.labelText = strings.TrimSpace(getItemAttrStringCfg(item, config, "label", ""))
	item.runtime.text = strings.TrimSpace(item.Text)
	item.runtime.specialFormat = prepareRenderSpecialFormatRuntime(item)
	prepareRenderTypeRuntime(config, item)
	item.runtime.prepared = true
	defaultPoints := defaultRenderHistoryPoints(item.Type)
	if defaultPoints <= 0 {
		return
	}
	item.runtime.historyKey = buildRenderHistoryKey(item)
	item.runtime.historyPoints = resolveItemHistoryPoints(item, config, defaultPoints)
}

func appendRenderHistory(store *renderHistoryStore, item *ItemConfig, value float64) []float64 {
	if store == nil || item == nil {
		return []float64{value}
	}
	if item.runtime.historyKey == "" || item.runtime.historyPoints <= 0 {
		return []float64{value}
	}
	return store.append(item.runtime.historyKey, value, item.runtime.historyPoints)
}

func appendFrameRenderHistory(frame *RenderFrame, item *ItemConfig, value float64) []float64 {
	if frame == nil {
		return []float64{value}
	}
	return appendRenderHistory(frame.history, item, value)
}

func resolveItemTitleText(item *ItemConfig, config *MonitorConfig) string {
	if item == nil {
		return ""
	}
	if item.runtime.prepared {
		return item.runtime.titleText
	}
	return strings.TrimSpace(getItemAttrStringCfg(item, config, "title", ""))
}

func resolveItemLabelText(item *ItemConfig, config *MonitorConfig) string {
	if item == nil {
		return ""
	}
	if item.runtime.prepared {
		return item.runtime.labelText
	}
	return strings.TrimSpace(getItemAttrStringCfg(item, config, "label", ""))
}

func resolveItemText(item *ItemConfig) string {
	if item == nil {
		return ""
	}
	if item.runtime.prepared {
		return item.runtime.text
	}
	return strings.TrimSpace(item.Text)
}

func prepareRenderTypeRuntime(config *MonitorConfig, item *ItemConfig) {
	if item == nil {
		return
	}
	switch item.Type {
	case itemTypeSimpleChart:
		item.runtime.simpleChart.lineWidth = clampRenderFloat(getItemAttrFloatCfg(item, config, "line_width", 1.5), 1)
		item.runtime.simpleChart.enableThresholdColors = getItemAttrBoolCfg(item, config, "enable_threshold_colors", false)
	case itemTypeFullChart:
		item.runtime.fullCard = prepareRenderFullCardRuntime(config, item, 4)
		item.runtime.fullChart.lineColor = resolveFullChartLineColor(item, config)
		item.runtime.fullChart.fillColor = getItemAttrColorCfg(item, config, "chart_fill_color", "rgba(0,0,0,0)")
		item.runtime.fullChart.chartAreaBg = getItemAttrColorCfg(item, config, "chart_area_bg", "")
		item.runtime.fullChart.chartAreaBorder = getItemAttrColorCfg(item, config, "chart_area_border_color", "")
		item.runtime.fullChart.showSegmentLines = getItemAttrBoolCfg(
			item,
			config,
			"show_segment_lines",
			getItemAttrBoolCfg(item, config, "show_grid_lines", true),
		)
		item.runtime.fullChart.gridLines = clampRenderInt(getItemAttrIntCfg(item, config, "grid_lines", 4), 2)
		item.runtime.fullChart.lineWidth = clampRenderFloat(getItemAttrFloatCfg(item, config, "line_width", 2), 1)
		item.runtime.fullChart.enableThresholdColors = getItemAttrBoolCfg(item, config, "enable_threshold_colors", false)
		item.runtime.fullChart.showAvgLine = getItemAttrBoolCfg(item, config, "show_avg_line", true)
	case itemTypeFullTable:
		item.runtime.fullCard = prepareRenderFullCardRuntime(config, item, 4)
		item.runtime.fullTable = prepareRenderFullTableRuntime(item, config)
	case itemTypeFullProgressH, itemTypeFullProgressV:
		item.runtime.fullCard = prepareRenderFullCardRuntime(config, item, 0)
		item.runtime.fullProgress.style = normalizeFullProgressStyle(getItemAttrStringCfg(item, config, "progress_style", "gradient"))
		item.runtime.fullProgress.barRadius = getItemAttrFloatCfg(item, config, "bar_radius", 0)
		if item.runtime.fullProgress.barRadius <= 0 {
			item.runtime.fullProgress.barRadius = item.runtime.radius
		}
		if item.runtime.fullProgress.barRadius < 0 {
			item.runtime.fullProgress.barRadius = 0
		}
		item.runtime.fullProgress.barHeight = getItemAttrFloatCfg(item, config, "bar_height", 0)
		item.runtime.fullProgress.trackColor = getItemAttrColorCfg(item, config, "track_color", "#1f2937")
		item.runtime.fullProgress.segments = clampRenderInt(getItemAttrIntCfg(item, config, "segments", 12), 4)
		item.runtime.fullProgress.segmentGap = getItemAttrFloatCfg(item, config, "segment_gap", 2)
	case itemTypeFullGauge:
		item.runtime.fullGauge.thickness = getItemAttrFloatCfg(item, config, "gauge_thickness", 10)
		item.runtime.fullGauge.gapDegrees = getItemAttrFloatCfg(item, config, "gauge_gap_degrees", 76)
		item.runtime.fullGauge.trackColor = getItemAttrColorCfg(item, config, "track_color", "#1f2937")
		item.runtime.fullGauge.textGap = getItemAttrFloatCfg(item, config, "gauge_text_gap", 1)
	case itemTypeSimpleLine:
		item.runtime.simpleLine.orientation = normalizeSimpleLineOrientation(getItemAttrStringCfg(item, config, "line_orientation", "horizontal"))
		item.runtime.simpleLine.lineWidth = clampRenderFloat(getItemAttrFloatCfg(item, config, "line_width", 1), 1)
	}
}

func normalizeRenderLevelColors(colors []string) []string {
	if len(colors) == 0 {
		return nil
	}
	normalized := append([]string(nil), colors...)
	for len(normalized) < 4 {
		normalized = append(normalized, normalized[len(normalized)-1])
	}
	if len(normalized) > 4 {
		normalized = normalized[:4]
	}
	return normalized
}

func prepareRenderFullCardRuntime(config *MonitorConfig, item *ItemConfig, defaultBodyGap float64) renderFullCardRuntime {
	return renderFullCardRuntime{
		bodyGap:             clampMinFloat(getItemAttrFloatCfg(item, config, "body_gap", defaultBodyGap), 0),
		headerHeight:        getItemAttrIntCfg(item, config, "header_height", 0),
		headerDivider:       getItemAttrBoolCfg(item, config, "header_divider", true),
		headerDividerColor:  getItemAttrColorCfg(item, config, "header_divider_color", "#94a3b840"),
		headerDividerWidth:  clampRenderFloat(getItemAttrFloatCfg(item, config, "header_divider_width", 1), 1),
		headerDividerOffset: clampMinFloat(getItemAttrFloatCfg(item, config, "header_divider_offset", 3), 0),
	}
}

func normalizeFullProgressStyle(style string) string {
	style = strings.ToLower(strings.TrimSpace(style))
	if style == "glow" {
		return "gradient"
	}
	switch style {
	case "", "gradient", "solid", "segmented", "stripes":
		if style == "" {
			return "gradient"
		}
		return style
	}
	return "gradient"
}

func normalizeSimpleLineOrientation(orientation string) string {
	orientation = strings.ToLower(strings.TrimSpace(orientation))
	if orientation == "vertical" {
		return orientation
	}
	return "horizontal"
}

func clampRenderFloat(value float64, minValue float64) float64 {
	if value < minValue {
		return minValue
	}
	return value
}

func clampMinFloat(value float64, minValue float64) float64 {
	if value < minValue {
		return minValue
	}
	return value
}

func clampRenderInt(value int, minValue int) int {
	if value < minValue {
		return minValue
	}
	return value
}

func fullResolveTextParts(item *ItemConfig, monitor *RenderMonitorSnapshot, value *CollectValue, config *MonitorConfig) (string, string, string) {
	labelText := resolveItemTitleText(item, config)
	if labelText == "" {
		labelText = resolveItemLabelText(item, config)
	}
	if labelText == "" && monitor != nil {
		labelText = strings.TrimSpace(monitor.label)
	}
	if labelText == "" {
		labelText = strings.TrimSpace(item.Monitor)
	}
	valueText, unitText := resolveItemDisplayValueParts(item, monitor, value, config)
	return labelText, valueText, unitText
}

func fullResolveTexts(item *ItemConfig, monitor *RenderMonitorSnapshot, value *CollectValue, config *MonitorConfig) (string, string) {
	labelText, valueText, unitText := fullResolveTextParts(item, monitor, value, config)
	displayValue := strings.TrimSpace(valueText + " " + unitText)
	if displayValue == "" {
		displayValue = strings.TrimSpace(valueText)
	}
	return labelText, displayValue
}

func resolveFullChartLineColor(item *ItemConfig, config *MonitorConfig) string {
	if item != nil && item.runtime.prepared && strings.TrimSpace(item.runtime.fullChart.lineColor) != "" {
		return item.runtime.fullChart.lineColor
	}
	attrColor := strings.TrimSpace(getItemAttrColorCfg(item, config, "chart_color", ""))
	if attrColor != "" {
		return attrColor
	}
	if item != nil {
		if color := strings.TrimSpace(resolveStyleColor(item, config, "color", "")); color != "" {
			return color
		}
	}
	return "#38bdf8"
}

func resolveChartThresholdColor(value float64, thresholds []float64, colors []string, fallback string) string {
	if len(thresholds) == 0 || len(colors) == 0 {
		return fallback
	}
	for idx, threshold := range thresholds {
		if value <= threshold {
			if idx < len(colors) {
				return colors[idx]
			}
			return colors[len(colors)-1]
		}
	}
	return colors[len(colors)-1]
}

func fullBuildHeaderAndBody(
	item *ItemConfig,
	config *MonitorConfig,
	fontCache *FontCache,
	labelText string,
	displayValue string,
	contentPaddingX float64,
	contentPaddingY float64,
	defaultBodyGap float64,
) (fullRect, fullRect, font.Face, font.Face) {
	labelFace, _ := resolveRoleFontFace(fontCache, item, config, TextRoleTitle, 16, 8)
	valueFace, _ := resolveRoleFontFace(fontCache, item, config, TextRoleValue, 18, 8)

	bodyGap := resolveFullCardBodyGap(item, config, defaultBodyGap)

	labelMetrics := baseMeasureText(labelFace, labelText)
	valueMetrics := baseMeasureText(valueFace, displayValue)
	headerTopPadding := 2.0
	headerBottomPadding := 2.0
	headerAscent := math.Max(labelMetrics.ascent, valueMetrics.ascent)
	headerDescent := math.Max(labelMetrics.descent, valueMetrics.descent)
	autoHeaderHeight := int(math.Ceil(headerAscent + headerDescent + headerTopPadding + headerBottomPadding))
	if autoHeaderHeight < 10 {
		autoHeaderHeight = 10
	}

	headerHeight := resolveFullCardHeaderHeight(item, config)
	if headerHeight <= 0 {
		headerHeight = autoHeaderHeight
	}
	if headerHeight < 1 {
		headerHeight = 1
	}
	availableHeight := int(math.Floor(float64(item.Height) - contentPaddingY*2 - bodyGap - 1))
	if availableHeight < 1 {
		availableHeight = 1
	}
	if headerHeight > availableHeight {
		headerHeight = availableHeight
	}

	headerRect := fullRect{
		x: float64(item.X) + contentPaddingX,
		y: float64(item.Y) + contentPaddingY,
		w: float64(item.Width) - contentPaddingX*2,
		h: float64(headerHeight),
	}
	if headerRect.w < 1 {
		headerRect.w = 1
	}
	if headerRect.h < 1 {
		headerRect.h = 1
	}

	bodyRect := fullRect{
		x: float64(item.X) + contentPaddingX,
		y: headerRect.y + headerRect.h + bodyGap,
		w: float64(item.Width) - contentPaddingX*2,
		h: float64(item.Height) - contentPaddingY*2 - (headerRect.h + bodyGap),
	}
	if bodyRect.w < 1 {
		bodyRect.w = 1
	}
	if bodyRect.h < 1 {
		bodyRect.h = 1
	}

	return headerRect, bodyRect, labelFace, valueFace
}

func drawFullHeader(
	dc *gg.Context,
	item *ItemConfig,
	config *MonitorConfig,
	rect fullRect,
	labelFace font.Face,
	valueFace font.Face,
	labelText string,
	valueText string,
	labelColor string,
	valueColor string,
) {
	headerCenterY := rect.y + rect.h/2
	const headerHorizontalPadding = 2.0

	dc.SetColor(parseColor(labelColor))
	drawBaseMetricAnchoredText(dc, labelFace, labelText, rect.x+headerHorizontalPadding, headerCenterY, 0)

	dc.SetColor(parseColor(valueColor))
	drawBaseMetricAnchoredText(dc, valueFace, valueText, rect.x+rect.w-headerHorizontalPadding, headerCenterY, 1)

	divider := resolveFullCardHeaderDivider(item, config)
	if divider {
		dc.SetColor(parseColor(resolveFullCardHeaderDividerColor(item, config)))
		dividerWidth := resolveFullCardHeaderDividerWidth(item, config)
		dc.SetLineWidth(dividerWidth)
		dividerOffset := resolveFullCardHeaderDividerOffset(item, config)
		y := rect.y + rect.h + dividerOffset
		dc.DrawLine(rect.x, y, rect.x+rect.w, y)
		dc.Stroke()
	}
}

func drawFullHeaderValueWithUnit(
	dc *gg.Context,
	rect fullRect,
	valueFace font.Face,
	unitFace font.Face,
	valueText string,
	unitText string,
	valueColor string,
	unitColor string,
) {
	const headerHorizontalPadding = 2.0
	rightX := rect.x + rect.w - headerHorizontalPadding
	centerY := rect.y + rect.h/2

	if strings.TrimSpace(unitText) == "" {
		dc.SetColor(parseColor(valueColor))
		drawBaseMetricAnchoredText(dc, valueFace, valueText, rightX, centerY, 1)
		return
	}

	dc.SetFontFace(valueFace)
	valueWidth, _ := dc.MeasureString(valueText)
	dc.SetFontFace(unitFace)
	unitWidth, _ := dc.MeasureString(unitText)

	gap := 2.0
	totalWidth := valueWidth + unitWidth + gap
	valueX := rightX - totalWidth
	unitX := valueX + valueWidth + gap

	dc.SetColor(parseColor(valueColor))
	drawBaseMetricAnchoredText(dc, valueFace, valueText, valueX, centerY, 0)
	dc.SetColor(parseColor(unitColor))
	drawBaseMetricAnchoredText(dc, unitFace, unitText, unitX, centerY, 0)
}

func resolveFullCardBodyGap(item *ItemConfig, config *MonitorConfig, fallback float64) float64 {
	if item != nil && item.runtime.prepared {
		return item.runtime.fullCard.bodyGap
	}
	return clampMinFloat(getItemAttrFloatCfg(item, config, "body_gap", fallback), 0)
}

func resolveFullCardHeaderHeight(item *ItemConfig, config *MonitorConfig) int {
	if item != nil && item.runtime.prepared {
		return item.runtime.fullCard.headerHeight
	}
	return getItemAttrIntCfg(item, config, "header_height", 0)
}

func resolveFullCardHeaderDivider(item *ItemConfig, config *MonitorConfig) bool {
	if item != nil && item.runtime.prepared {
		return item.runtime.fullCard.headerDivider
	}
	return getItemAttrBoolCfg(item, config, "header_divider", true)
}

func resolveFullCardHeaderDividerColor(item *ItemConfig, config *MonitorConfig) string {
	if item != nil && item.runtime.prepared && item.runtime.fullCard.headerDividerColor != "" {
		return item.runtime.fullCard.headerDividerColor
	}
	return getItemAttrColorCfg(item, config, "header_divider_color", "#94a3b840")
}

func resolveFullCardHeaderDividerWidth(item *ItemConfig, config *MonitorConfig) float64 {
	if item != nil && item.runtime.prepared {
		return item.runtime.fullCard.headerDividerWidth
	}
	return clampRenderFloat(getItemAttrFloatCfg(item, config, "header_divider_width", 1), 1)
}

func resolveFullCardHeaderDividerOffset(item *ItemConfig, config *MonitorConfig) float64 {
	if item != nil && item.runtime.prepared {
		return item.runtime.fullCard.headerDividerOffset
	}
	return clampMinFloat(getItemAttrFloatCfg(item, config, "header_divider_offset", 3), 0)
}

func normalizeRatio(value float64, minValue float64, maxValue float64) float64 {
	if maxValue <= minValue {
		return 0
	}
	progress := (value - minValue) / (maxValue - minValue)
	if progress < 0 {
		return 0
	}
	if progress > 1 {
		return 1
	}
	return progress
}

func historyMin(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	result := 0.0
	valid := false
	for _, value := range values {
		if !isFiniteHistoryValue(value) {
			continue
		}
		if !valid {
			result = value
			valid = true
			continue
		}
		if value < result {
			result = value
		}
	}
	if !valid {
		return 0
	}
	return result
}

func historyMax(values []float64) float64 {
	if len(values) == 0 {
		return 1
	}
	result := 0.0
	valid := false
	for _, value := range values {
		if !isFiniteHistoryValue(value) {
			continue
		}
		if !valid {
			result = value
			valid = true
			continue
		}
		if value > result {
			result = value
		}
	}
	if !valid {
		return 1
	}
	return result
}

func historyAverage(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sum := 0.0
	count := 0
	for _, value := range values {
		if !isFiniteHistoryValue(value) {
			continue
		}
		sum += value
		count++
	}
	if count == 0 {
		return 0
	}
	return sum / float64(count)
}

func applyAlpha(colorText string, alpha float64) string {
	parsed := color.NRGBAModel.Convert(parseColor(colorText)).(color.NRGBA)
	if alpha < 0 {
		alpha = 0
	}
	if alpha > 1 {
		alpha = 1
	}
	return fmt.Sprintf("#%02x%02x%02x%02x", parsed.R, parsed.G, parsed.B, uint8(alpha*255))
}

func drawRoundedRectFill(dc *gg.Context, x, y, width, height, radius float64, colorText string) {
	if width <= 0 || height <= 0 {
		return
	}
	if radius < 0 {
		radius = 0
	}
	if radius > height/2 {
		radius = height / 2
	}
	dc.SetColor(parseColor(colorText))
	if radius > 0 {
		dc.DrawRoundedRectangle(x, y, width, height, radius)
	} else {
		dc.DrawRectangle(x, y, width, height)
	}
	dc.Fill()
}

func drawMetricAnchoredText(dc *gg.Context, face font.Face, text string, x, centerY, anchorX float64) {
	drawBaseMetricAnchoredText(dc, face, text, x, centerY, anchorX)
}
