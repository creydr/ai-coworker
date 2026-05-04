package engine

import (
	"context"
	"fmt"
	"strings"

	"github.com/creydr/ai-coworker/internal/domain"
	"github.com/creydr/ai-coworker/internal/llm"
)

// IntentClassifier uses an LLM provider to determine the intent behind an incoming event.
type IntentClassifier struct {
	provider llm.Provider
}

// NewIntentClassifier creates a new IntentClassifier backed by the given LLM provider.
func NewIntentClassifier(provider llm.Provider) *IntentClassifier {
	return &IntentClassifier{provider: provider}
}

// Classify determines the intent of an incoming event. It short-circuits for
// known review events and falls back to LLM-based classification otherwise.
func (c *IntentClassifier) Classify(ctx context.Context, event domain.IncomingEvent, history []domain.Message) (domain.Intent, error) {
	// Short-circuit: events with review_state metadata or review_comment type
	// are always review intents.
	if _, ok := event.Metadata["review_state"]; ok {
		return domain.IntentReview, nil
	}
	if event.Metadata["type"] == "review_comment" {
		return domain.IntentReview, nil
	}

	// Build a prompt for the LLM to classify the intent.
	var sb strings.Builder
	sb.WriteString("You are an intent classifier. Given the following conversation, classify the latest message into exactly one of these categories:\n")
	sb.WriteString("- code_task: the user is requesting code changes, implementation, or a coding task\n")
	sb.WriteString("- review: the user is requesting a code review\n")
	sb.WriteString("- question: the user is asking a question that needs an answer\n")
	sb.WriteString("- discussion: the user wants to discuss or brainstorm something\n\n")

	if len(history) > 0 {
		sb.WriteString("Conversation history:\n")
		for _, msg := range history {
			fmt.Fprintf(&sb, "[%s]: %s\n", msg.Role, msg.Content)
		}
		sb.WriteString("\n")
	}

	fmt.Fprintf(&sb, "Latest message: %s\n\n", event.Content)
	sb.WriteString("Respond with exactly one word: code_task, review, question, or discussion.")

	messages := []llm.Message{
		{Role: domain.RoleUser, Content: sb.String()},
	}

	response, err := c.provider.Chat(ctx, messages)
	if err != nil {
		return domain.IntentUnknown, fmt.Errorf("classifying intent: %w", err)
	}

	return parseIntent(response), nil
}

// parseIntent extracts a domain.Intent from the raw LLM response text.
func parseIntent(response string) domain.Intent {
	normalized := strings.ToLower(strings.TrimSpace(response))

	switch normalized {
	case "code_task":
		return domain.IntentCodeTask
	case "review":
		return domain.IntentReview
	case "question":
		return domain.IntentQuestion
	case "discussion":
		return domain.IntentDiscussion
	default:
		return domain.IntentUnknown
	}
}
