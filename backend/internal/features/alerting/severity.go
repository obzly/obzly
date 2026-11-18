package alerting

import "strings"

// Rule severities, stored lowercase.
const (
	SeverityCritical = "critical"
	SeverityHigh     = "high"
	SeverityMedium   = "medium"
	SeverityLow      = "low"
)

// Emoji per severity and status, exposed to templates as .Emoji.
const (
	EmojiCriticalFiring = "\U0001F4E3❗\U0001F525"          // 📣❗🔥
	EmojiHighFiring     = "\U0001F4E3❗\U0001F6A8"          // 📣❗🚨
	EmojiMediumFiring   = "\U0001F4E3 \U0001F514⚠️"        // 📣 🔔⚠️
	EmojiLowFiring      = "\U0001F514\U0001F514\U0001F440" // 🔔🔔👀
	EmojiResolved       = "✅️"                             // ✅️
	EmojiTest           = "\U0001F9EA"                     // 🧪
)

// Slack side-bar colours: fixed status colours, the same in every theme.
const (
	colorCritical = "#DC2626"
	colorHigh     = "#EA580C"
	colorMedium   = "#F59E0B"
	colorLow      = "#2563EB"
	colorResolved = "#16A34A"
	colorTest     = "#6B7280"
)

var severities = []string{SeverityCritical, SeverityHigh, SeverityMedium, SeverityLow}

// SeverityFromLabel maps a rule's severity label to a severity, as the
// migration did for existing rules; false when the label says nothing.
func SeverityFromLabel(label string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(label)) {
	case "critical":
		return SeverityCritical, true
	case "high", "error":
		return SeverityHigh, true
	case "warning", "medium":
		return SeverityMedium, true
	case "info", "low":
		return SeverityLow, true
	}
	return "", false
}

// ValidSeverity reports whether s is one of the four severities.
func ValidSeverity(s string) bool {
	for _, known := range severities {
		if s == known {
			return true
		}
	}
	return false
}

// normalizeSeverity is s if valid, else medium (old outbox payloads have none).
func normalizeSeverity(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if ValidSeverity(s) {
		return s
	}
	return SeverityMedium
}

func severityTitle(s string) string {
	s = normalizeSeverity(s)
	return strings.ToUpper(s[:1]) + s[1:]
}

// pagerDutySeverity is PagerDuty's Events v2 severity for s.
func pagerDutySeverity(s string) string {
	switch normalizeSeverity(s) {
	case SeverityCritical:
		return "critical"
	case SeverityHigh:
		return "error"
	case SeverityLow:
		return "info"
	}
	return "warning"
}

// emojiFor is the emoji of a notification with status and severity.
func emojiFor(status, severity string) string {
	switch status {
	case StatusResolved:
		return EmojiResolved
	case StatusTest:
		return EmojiTest
	}
	switch normalizeSeverity(severity) {
	case SeverityCritical:
		return EmojiCriticalFiring
	case SeverityHigh:
		return EmojiHighFiring
	case SeverityLow:
		return EmojiLowFiring
	}
	return EmojiMediumFiring
}

// slackColor is the side bar of a Slack message.
func slackColor(status, severity string) string {
	switch status {
	case StatusResolved:
		return colorResolved
	case StatusTest:
		return colorTest
	}
	switch normalizeSeverity(severity) {
	case SeverityCritical:
		return colorCritical
	case SeverityHigh:
		return colorHigh
	case SeverityLow:
		return colorLow
	}
	return colorMedium
}

// ruleSeverity is the severity a rule gets when none is given: from its
// severity label, else medium.
func ruleSeverity(requested string, labels Labels) string {
	requested = strings.ToLower(strings.TrimSpace(requested))
	if ValidSeverity(requested) {
		return requested
	}
	if mapped, ok := SeverityFromLabel(labels["severity"]); ok {
		return mapped
	}
	return SeverityMedium
}

// managedSeriesSeverity maps the severity label a managed rule's series
// carries (posture findings, AI budget thresholds); false when it has none.
func managedSeriesSeverity(label string) (string, bool) {
	if strings.EqualFold(strings.TrimSpace(label), "informational") {
		return SeverityLow, true
	}
	return SeverityFromLabel(label)
}
