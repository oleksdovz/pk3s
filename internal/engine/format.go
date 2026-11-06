package engine

import "fmt"

// FormatEvent renders an event as a single log line.
func FormatEvent(ev Event) string {
	switch ev.Kind {
	case EventStepStart:
		return fmt.Sprintf("[%d/%d] START %s", ev.Index+1, ev.Total, ev.Title)
	case EventStepDone:
		return fmt.Sprintf("[%d/%d] DONE  %s", ev.Index+1, ev.Total, ev.Title)
	case EventStepSkipped:
		return fmt.Sprintf("[%d/%d] SKIP  %s (already done)", ev.Index+1, ev.Total, ev.Title)
	case EventStepFailed:
		return fmt.Sprintf("[%d/%d] FAIL  %s: %s", ev.Index+1, ev.Total, ev.Title, ev.Message)
	case EventFinished:
		return "all steps completed"
	default:
		return "      " + ev.Message
	}
}
