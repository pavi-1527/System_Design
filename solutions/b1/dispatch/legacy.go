package dispatch

// legacy.go is GIVEN: the notification dispatcher as it exists today. Do not
// edit it. It is here to be read, and to be replaced.
//
// Collect the smells before you start: errors are strings; "should we retry?"
// is decided by looking for the word "timeout" in a message; the sink registry
// and the statistics are package-level maps with no lock; the retry delays are
// hard-coded sleeps, so nothing that retries can be tested quickly; there is no
// context, so a hung sink hangs the caller.

import (
	"log"
	"strings"
	"time"
)

// LegacySinks maps a sink name to a function that returns "" on success or a
// human-readable error text. Unsynchronised.
var LegacySinks = map[string]func(payload string) string{}

// LegacyStats counts "<sink>:ok" and "<sink>:fail". Unsynchronised.
var LegacyStats = map[string]int{}

// LegacyDispatch delivers one payload to one named sink and returns "ok" or
// "error: <text>". The event type is only used for logging.
func LegacyDispatch(sink, eventType, payload string) string {
	f, ok := LegacySinks[sink]
	if !ok {
		return "error: no such sink: " + sink
	}
	res := ""
	for attempt := 0; attempt < 3; attempt++ {
		res = f(payload)
		if res == "" {
			LegacyStats[sink+":ok"]++
			log.Printf("%s delivered %s to %s", time.Now().Format(time.RFC3339), eventType, sink)
			return "ok"
		}
		if !strings.Contains(res, "timeout") {
			break // any other text is "permanent"
		}
		time.Sleep(time.Duration(100<<attempt) * time.Millisecond)
	}
	LegacyStats[sink+":fail"]++
	return "error: " + res
}
