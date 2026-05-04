package engine

import (
	"testing"

	"github.com/creydr/ai-coworker/internal/domain"
)

func TestParseIntent(t *testing.T) {
	tests := []struct {
		name     string
		response string
		want     domain.Intent
	}{
		// Exact matches
		{name: "exact code_task", response: "code_task", want: domain.IntentCodeTask},
		{name: "exact review", response: "review", want: domain.IntentReview},
		{name: "exact question", response: "question", want: domain.IntentQuestion},
		{name: "exact discussion", response: "discussion", want: domain.IntentDiscussion},

		// Case insensitivity
		{name: "uppercase CODE_TASK", response: "CODE_TASK", want: domain.IntentCodeTask},
		{name: "mixed case Review", response: "Review", want: domain.IntentReview},
		{name: "mixed case Question", response: "Question", want: domain.IntentQuestion},
		{name: "mixed case Discussion", response: "DISCUSSION", want: domain.IntentDiscussion},

		// Whitespace trimming
		{name: "leading/trailing spaces", response: "  code_task  ", want: domain.IntentCodeTask},
		{name: "trailing newline", response: "review\n", want: domain.IntentReview},
		{name: "leading tab", response: "\tquestion", want: domain.IntentQuestion},

		// Should NOT match when embedded in a sentence (the bug we're fixing)
		{name: "code_task in sentence", response: "I don't think this is a code_task", want: domain.IntentUnknown},
		{name: "review in sentence", response: "this is not a review request", want: domain.IntentUnknown},
		{name: "question in sentence", response: "the user asked a question here", want: domain.IntentUnknown},
		{name: "discussion in sentence", response: "let's have a discussion about it", want: domain.IntentUnknown},

		// Unknown/empty
		{name: "empty string", response: "", want: domain.IntentUnknown},
		{name: "random text", response: "something_else", want: domain.IntentUnknown},
		{name: "partial match", response: "code_tas", want: domain.IntentUnknown},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseIntent(tt.response)
			if got != tt.want {
				t.Errorf("parseIntent(%q) = %q, want %q", tt.response, got, tt.want)
			}
		})
	}
}
