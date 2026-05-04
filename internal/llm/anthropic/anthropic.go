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
	resp, err := p.client.Messages.New(ctx, anthropic.MessageNewParams{
		MaxTokens: 4096,
		Model:     anthropic.Model(p.model),
		Messages:  convertMessages(messages),
	})
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

func convertMessages(messages []llm.Message) []anthropic.MessageParam {
	params := make([]anthropic.MessageParam, 0, len(messages))
	for _, msg := range messages {
		switch msg.Role {
		case domain.RoleUser:
			params = append(params, anthropic.NewUserMessage(anthropic.NewTextBlock(msg.Content)))
		case domain.RoleAssistant:
			params = append(params, anthropic.NewAssistantMessage(anthropic.NewTextBlock(msg.Content)))
		}
	}
	return params
}
