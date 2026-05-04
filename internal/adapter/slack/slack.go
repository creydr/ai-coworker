package slack

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/creydr/ai-coworker/internal/adapter"
	"github.com/creydr/ai-coworker/internal/domain"
	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"
)

type Adapter struct {
	client       *slack.Client
	socketClient *socketmode.Client
	botUserID    string
}

func New(appToken, botToken string) *Adapter {
	client := slack.New(
		botToken,
		slack.OptionAppLevelToken(appToken),
	)
	socketClient := socketmode.New(client)

	return &Adapter{
		client:       client,
		socketClient: socketClient,
	}
}

func (a *Adapter) Name() string {
	return "slack"
}

func (a *Adapter) Start(ctx context.Context, handler adapter.EventHandler) error {
	authResp, err := a.client.AuthTestContext(ctx)
	if err != nil {
		return fmt.Errorf("slack auth test failed: %w", err)
	}
	a.botUserID = authResp.UserID

	go func() {
		for evt := range a.socketClient.Events {
			if evt.Type != socketmode.EventTypeEventsAPI {
				continue
			}

			eventsAPIEvent, ok := evt.Data.(slackevents.EventsAPIEvent)
			if !ok {
				continue
			}

			a.socketClient.Ack(*evt.Request)

			if eventsAPIEvent.InnerEvent.Type != string(slackevents.AppMention) {
				continue
			}

			mentionEvent, ok := eventsAPIEvent.InnerEvent.Data.(*slackevents.AppMentionEvent)
			if !ok {
				continue
			}

			text := stripBotMention(mentionEvent.Text, a.botUserID)

			threadTS := mentionEvent.ThreadTimeStamp
			if threadTS == "" {
				threadTS = mentionEvent.TimeStamp
			}

			threadID := fmt.Sprintf("slack-%s-%s", mentionEvent.Channel, threadTS)

			incomingEvent := domain.IncomingEvent{
				Channel:    "slack",
				ChannelRef: NewRef(mentionEvent.Channel, threadTS),
				ThreadID:   threadID,
				UserID:     mentionEvent.User,
				Content:    text,
			}

			if err := handler(ctx, incomingEvent); err != nil {
				slog.Error("error handling slack event", "error", err)
			}
		}
	}()

	return a.socketClient.RunContext(ctx)
}

func (a *Adapter) SendResponse(ctx context.Context, ref domain.ChannelRef, message string) error {
	s := ParseRef(ref)
	_, _, err := a.client.PostMessageContext(
		ctx,
		s.ChannelID,
		slack.MsgOptionText(message, false),
		slack.MsgOptionTS(s.ThreadTS),
	)
	if err != nil {
		return fmt.Errorf("failed to post slack message: %w", err)
	}
	return nil
}

func (a *Adapter) Acknowledge(ctx context.Context, ref domain.ChannelRef) error {
	s := ParseRef(ref)
	err := a.client.AddReactionContext(ctx, "eyes", slack.ItemRef{
		Channel:   s.ChannelID,
		Timestamp: s.ThreadTS,
	})
	if err != nil {
		return fmt.Errorf("failed to add slack reaction: %w", err)
	}
	return nil
}

func stripBotMention(text, botUserID string) string {
	mention := fmt.Sprintf("<@%s>", botUserID)
	text = strings.Replace(text, mention, "", 1)
	return strings.TrimSpace(text)
}

// Compile-time check that Adapter implements adapter.Adapter.
var _ adapter.Adapter = (*Adapter)(nil)
