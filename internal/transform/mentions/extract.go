package mentions

import (
	"regexp"
	"strings"
)

// mentionPattern builds a case-sensitive matcher for `\b(<alt1>|<alt2>|...)-[0-9a-z]+(\.[0-9]+)*\b`
// over exactly the given prefixes (AC-29's pattern). \b only tests position and never consumes a
// character, so a trailing sentence period after an id is never part of a match, while a dotted
// child suffix such as ".1" in "trk-bam.1" is, because the optional "(\.[0-9]+)*" group greedily
// consumes any run of ".digits" immediately following the id body.
func mentionPattern(prefixes []string) *regexp.Regexp {
	alts := make([]string, len(prefixes))
	for i, p := range prefixes {
		alts[i] = regexp.QuoteMeta(p)
	}
	pattern := `\b(?:` + strings.Join(alts, "|") + `)-[0-9a-z]+(?:\.[0-9]+)*\b`
	return regexp.MustCompile(pattern)
}

// extractMentions scans text for references to any issue id whose prefix is in prefixes,
// matching \b(<alt1>|<alt2>|...)-[0-9a-z]+(\.[0-9]+)*\b case-sensitively (AC-29's pattern,
// including child ids such as "trk-bam.1"; a trailing sentence period is never part of a
// match, since \b only tests position and does not consume it), and returns a count of
// matches per mentioned issue id. selfID's own id is excluded (a self-mention), and any
// prefix outside prefixes never matches at all, because the alternation is built only from
// prefixes -- there is no separate "is this prefix known" filter to get wrong. A mentioned id
// with zero matches is never present as a key in the result.
func extractMentions(text, selfID string, prefixes []string) map[string]int64 {
	result := make(map[string]int64)
	if text == "" || len(prefixes) == 0 {
		return result
	}
	re := mentionPattern(prefixes)
	for _, m := range re.FindAllString(text, -1) {
		if m == selfID {
			continue
		}
		result[m]++
	}
	return result
}
