package types

import "strings"

// Image read prompts bound ordinary descriptions and reserve transcription for full reads.
const (
	ImageReadBriefPrompt = "Describe this image in at most 120 words. " +
		"Include its type, key labels, numbers and relationships. " + ImageReadChartPrompt +
		"Do not transcribe long passages. Say when content is illegible; do not guess. Plain text only."
	ImageReadFullPrompt = "Read this image closely. Transcribe readable text and all chart/table values; " +
		"describe diagram elements and relationships. " + ImageReadChartPrompt +
		"Mark illegible content rather than guessing. Plain text only."
	// Chart observations must preserve the spatial mapping lost by linear OCR.
	ImageReadChartPrompt = "For charts and tables, associate every reported value with its category, " +
		"series/legend and unit, using one labelled row per category. Distinguish axis tick labels from data values. " +
		"Never infer these associations from text reading order; mark ambiguous labels or values as unclear. " +
		"For plotted curves, read horizontal positions from the actual axis labels, not the report date. " +
		"Before estimating a value, locate its neighboring vertical ticks and interpolate its visible position. " +
		"Follow the requested trace left to right, preserving major troughs, peaks and rebounds, including shorter traces. " +
		"Keep observed values separate from estimates or calculations. "
	ImageReadFocusRunes = 300
)

// NormalizeImageRead supplies the brief default and bounds the optional focus.
func NormalizeImageRead(detail, focus string) (string, string) {
	if detail != ImageDetailFull {
		detail = ImageDetailBrief
	}
	focus = strings.TrimSpace(focus)
	if runes := []rune(focus); len(runes) > ImageReadFocusRunes {
		focus = string(runes[:ImageReadFocusRunes])
	}
	return detail, focus
}

// ImageReadPrompt is shared by attachment reads and generic tool-image fallback.
func ImageReadPrompt(detail, focus string) string {
	detail, focus = NormalizeImageRead(detail, focus)
	prompt := ImageReadBriefPrompt
	if detail == ImageDetailFull {
		prompt = ImageReadFullPrompt
	}
	if focus != "" {
		prompt += "\nThe assistant is looking for: " + focus +
			"\nReport the visible evidence needed for that request; avoid transcribing unrelated series or adding external explanations. " +
			"Say plainly when a mapping or value is unclear."
	}
	return prompt
}
