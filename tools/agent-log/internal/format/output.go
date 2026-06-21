package format

import (
	"encoding/json"
	"fmt"
	"strings"

	"agent-log/internal/agent"
)

// Human renders the agent result. When verbose is false, only the answer is printed.
// When verbose is true, all steps and chain summary are included.
func Human(res agent.Result, verbose bool) string {
	if !verbose {
		answer := strings.TrimSpace(res.Answer)
		if res.MaxTurns {
			answer += "\n[reached max_turns before final answer]"
		}
		return answer
	}

	var b strings.Builder
	for i, step := range res.Steps {
		b.WriteString(fmt.Sprintf("Step %d: %s\n", i+1, nonEmpty(step.Intent, "(query)")))
		if strings.TrimSpace(step.Command) != "" {
			b.WriteString(step.Command)
			b.WriteString("\n")
		}
		if strings.TrimSpace(step.Result) != "" {
			b.WriteString(step.Result)
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	b.WriteString("Finding: ")
	b.WriteString(strings.TrimSpace(res.Answer))
	b.WriteString("\n")

	b.WriteString(fmt.Sprintf("Chain: %d steps", len(res.Steps)))
	if len(res.Steps) != 0 {
		var intents []string
		for _, s := range res.Steps {
			if strings.TrimSpace(s.Intent) != "" {
				intents = append(intents, strings.TrimSpace(s.Intent))
			}
		}
		if len(intents) != 0 {
			b.WriteString(" — ")
			b.WriteString(strings.Join(intents, "; "))
		}
	}
	b.WriteString("\n")

	if res.MaxTurns {
		b.WriteString("Unknowns: reached max_turns before a final answer\n")
	}

	return strings.TrimSpace(b.String())
}

// JSON renders the raw result for --json mode.
func JSON(res agent.Result) (string, error) {
	body, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func nonEmpty(items ...string) string {
	for _, item := range items {
		if strings.TrimSpace(item) != "" {
			return item
		}
	}
	return ""
}
