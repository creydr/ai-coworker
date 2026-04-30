package claude

import (
	"context"
	"fmt"

	anthropic "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/creydr/ai-coworker/internal/llm"
)

type Provider struct {
	client *anthropic.Client
	model  string
}

func New(apiKey, model string) *Provider {
	client := anthropic.NewClient(option.WithAPIKey(apiKey))
	return &Provider{
		client: &client,
		model:  model,
	}
}

func (p *Provider) Chat(ctx context.Context, messages []llm.Message) (string, error) {
	resp, err := p.client.Messages.New(ctx, anthropic.MessageNewParams{
		MaxTokens: 4096,
		Model:     anthropic.Model(p.model),
		Messages:  convertMessages(messages),
	})
	if err != nil {
		return "", fmt.Errorf("claude chat: %w", err)
	}

	for _, block := range resp.Content {
		if block.Type == "text" {
			return block.Text, nil
		}
	}

	return "", fmt.Errorf("claude chat: no text block in response")
}

func convertMessages(messages []llm.Message) []anthropic.MessageParam {
	params := make([]anthropic.MessageParam, 0, len(messages))
	for _, msg := range messages {
		switch msg.Role {
		case llm.RoleUser:
			params = append(params, anthropic.NewUserMessage(anthropic.NewTextBlock(msg.Content)))
		case llm.RoleAssistant:
			params = append(params, anthropic.NewAssistantMessage(anthropic.NewTextBlock(msg.Content)))
		}
	}
	return params
}
