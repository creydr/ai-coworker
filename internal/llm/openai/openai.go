package openai

import (
	"context"
	"fmt"

	"github.com/creydr/ai-coworker/internal/llm"
	oai "github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
)

type Provider struct {
	client *oai.Client
	model  string
}

func New(baseURL, apiKey, model string) *Provider {
	opts := []option.RequestOption{}
	if baseURL != "" {
		opts = append(opts, option.WithBaseURL(baseURL))
	}
	if apiKey != "" {
		opts = append(opts, option.WithAPIKey(apiKey))
	}
	client := oai.NewClient(opts...)
	return &Provider{
		client: &client,
		model:  model,
	}
}

func (p *Provider) Chat(ctx context.Context, messages []llm.Message) (string, error) {
	resp, err := p.client.Chat.Completions.New(ctx, oai.ChatCompletionNewParams{
		Model:    p.model,
		Messages: convertMessages(messages),
	})
	if err != nil {
		return "", fmt.Errorf("openai chat: %w", err)
	}

	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("openai chat: no choices in response")
	}

	return resp.Choices[0].Message.Content, nil
}

func convertMessages(messages []llm.Message) []oai.ChatCompletionMessageParamUnion {
	params := make([]oai.ChatCompletionMessageParamUnion, 0, len(messages))
	for _, msg := range messages {
		switch msg.Role {
		case llm.RoleUser:
			params = append(params, oai.UserMessage(msg.Content))
		case llm.RoleAssistant:
			params = append(params, oai.AssistantMessage(msg.Content))
		}
	}
	return params
}
