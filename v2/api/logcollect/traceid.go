package logcollect

import "regexp"

// traceIDPattern finds lowercase 32-hex trace ids in the forms apps commonly
// log: trace_id=<hex>, traceId=<hex>, "trace_id":"<hex>", "traceId":"<hex>"
// and a W3C traceparent value 00-<32hex>-<16hex>-<2hex>.
var traceIDPattern = regexp.MustCompile(`\b(?:trace_id|traceId)=([0-9a-f]{32})\b|"(?:trace_id|traceId)"\s*:\s*"([0-9a-f]{32})"|\b00-([0-9a-f]{32})-[0-9a-f]{16}-[0-9a-f]{2}\b`)

// ExtractTraceID returns the first valid (not all zeros) trace id in a log
// line, or "".
func ExtractTraceID(line string) string {
	for _, match := range traceIDPattern.FindAllStringSubmatch(line, -1) {
		for _, id := range match[1:] {
			if id != "" && id != "00000000000000000000000000000000" {
				return id
			}
		}
	}
	return ""
}
