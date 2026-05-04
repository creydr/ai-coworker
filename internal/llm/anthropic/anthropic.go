package anthropic

import (
	"context"
	"fmt"

	anthropic "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/anthropics/anthropic-sdk-go/vertex"

	"github.com/creydr/ai-coworker/internal/domain"
	"github.com/creydr/ai-coworker/internal/llm"
)

const (
	// defaultMaxTokens is the maximum number of tokens the model will generate in a response.
	defaultMaxTokens = 4096
)

// Provider implements the LLM provider interface using the Anthropic API.
type Provider struct {
	client *anthropic.Client
	model  string
}

// New creates a new Anthropic provider with the given API key and model
func New(apiKey, model string) *Provider {
	client := anthropic.NewClient(option.WithAPIKey(apiKey))
	return &Provider{
		client: &client,
		model:  model,
	}
}

// NewVertex creates a new Anthropic provider that uses Vertex AI as the backend
func NewVertex(ctx context.Context, projectID, region, model string) *Provider {
	client := anthropic.NewClient(
		vertex.WithGoogleAuth(ctx, region, projectID),
	)
	return &Provider{
		client: &client,
		model:  model,
	}
}

func (p *Provider) Chat(ctx context.Context, messages []llm.Message) (string, error) {
	systemBlocks, chatMessages := convertMessages(messages)

	params := anthropic.MessageNewParams{
		MaxTokens: defaultMaxTokens,
		Model:     p.model,
		Messages:  chatMessages,
	}
	if len(systemBlocks) > 0 {
		params.System = systemBlocks
	}

	resp, err := p.client.Messages.New(ctx, params)
	if err != nil {
		return "", fmt.Errorf("anthropic chat: %w", err)
	}

	for _, block := range resp.Content {
		if block.Type == "text" {
			return block.Text, nil
		}
	}

	return "", fmt.Errorf("anthropic chat: no text block in response")
}

func convertMessages(messages []llm.Message) ([]anthropic.TextBlockParam, []anthropic.MessageParam) {
	var systemBlocks []anthropic.TextBlockParam
	params := make([]anthropic.MessageParam, 0, len(messages))

	for _, msg := range messages {
		switch msg.Role {
		case domain.RoleSystem:
			systemBlocks = append(systemBlocks, anthropic.TextBlockParam{Text: msg.Content})
		case domain.RoleUser:
			params = append(params, anthropic.NewUserMessage(anthropic.NewTextBlock(msg.Content)))
		case domain.RoleAssistant:
			params = append(params, anthropic.NewAssistantMessage(anthropic.NewTextBlock(msg.Content)))
		}
	}

	return systemBlocks, params
}
