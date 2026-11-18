package webanalytics

import (
	"regexp"
	"strings"
	"sync"

	"github.com/medama-io/go-useragent"
)

const (
	directReferrer = "(direct or none)"
	otherHost      = "(other hosts)"
	crawlers       = "Crawlers"
	unknownLabel   = "Other"
)

// scannerPaths: 404s that are vulnerability scanners probing for software
// the site doesn't run.
var scannerPaths = regexp.MustCompile(`(?i)(wp-login|wp-admin|wp-content|wp-includes|xmlrpc\.php|/\.env|/\.git|` +
	`phpmyadmin|/cgi-bin/|\.php$|/admin\.|/vendor/phpunit|/boaform|/actuator|/\.aws|/\.ssh|/config\.json|` +
	`/server-status|/owa/|/autodiscover|/solr/|/HNAP1|\.asp$|\.aspx$|/shell|/eval-stdin)`)

// searchReferrers: referring hosts that are search engines.
var searchReferrers = regexp.MustCompile(`(?i)(^|\.)(google|bing|duckduckgo|yahoo|yandex|baidu|ecosia|qwant|` +
	`startpage|brave|search)\.`)

var (
	uaParserOnce sync.Once
	uaParser     *useragent.Parser
)

// agent is what a User-Agent says about its browser, system and device.
type agent struct {
	browser, os, device string
}

// parseAgent classifies a User-Agent (crawlers by botPattern, the same rule
// the scan applies). The parser loads its definitions on first use.
func parseAgent(ua string, bot bool) agent {
	if bot {
		return agent{browser: crawlers, os: unknownLabel, device: "Bot"}
	}
	uaParserOnce.Do(func() { uaParser = useragent.NewParser() })
	parsed := uaParser.Parse(ua)
	result := agent{browser: string(parsed.Browser()), os: string(parsed.OS()), device: string(parsed.Device())}
	if parsed.IsBot() {
		return agent{browser: crawlers, os: unknownLabel, device: "Bot"}
	}
	if result.browser == "" {
		result.browser = unknownLabel
	}
	switch result.os {
	case "":
		result.os = unknownLabel
	case "MacOS":
		result.os = "macOS"
	}
	if result.device == "" {
		result.device = unknownLabel
	}
	return result
}

// notFoundTag tells a scanner's probe from a broken link.
func notFoundTag(path string, referred int64) string {
	switch {
	case scannerPaths.MatchString(path):
		return "scanner"
	case referred > 0:
		return "broken link"
	default:
		return ""
	}
}

// referrerTag marks search engines.
func referrerTag(host string) string {
	if host != directReferrer && searchReferrers.MatchString(host) {
		return "search"
	}
	return ""
}

// staticExtensionsText is the setting's stored form.
func staticExtensionsText(extensions []string) string {
	return strings.Join(extensions, " ")
}
