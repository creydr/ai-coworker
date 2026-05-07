package slack

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"

	"github.com/creydr/ai-coworker/internal/adapter"
	"github.com/creydr/ai-coworker/internal/domain"
)

// Adapter implements the adapter interface for Slack using Socket Mode
type Adapter struct {
	client       *slack.Client
	socketClient *socketmode.Client
	botUserID    string
}

// New creates a new Slack adapter with the given app-level and bot tokens
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

			a.socketClient.Ack(*evt.Request) //nolint:errcheck // best-effort ack

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

			if mentionEvent.ThreadTimeStamp != "" {
				threadContext := a.fetchThreadContext(ctx, mentionEvent.Channel, threadTS, mentionEvent.TimeStamp)
				if threadContext != "" {
					text = threadContext + "\n[Current message:]\n" + text
				}
			}

			threadID := fmt.Sprintf("slack-%s-%s", mentionEvent.Channel, threadTS)

			ref := NewRef(mentionEvent.Channel, threadTS)
			ref.Properties["message_ts"] = mentionEvent.TimeStamp

			incomingEvent := domain.IncomingEvent{
				ChannelRef: ref,
				ThreadID:   threadID,
				UserID:     mentionEvent.User,
				Content:    text,
			}

			if err := handler(ctx, []domain.IncomingEvent{incomingEvent}); err != nil {
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
	ts := s.MessageTS
	if ts == "" {
		ts = s.ThreadTS
	}
	err := a.client.AddReactionContext(ctx, "eyes", slack.ItemRef{
		Channel:   s.ChannelID,
		Timestamp: ts,
	})
	if err != nil {
		return fmt.Errorf("failed to add slack reaction: %w", err)
	}
	return nil
}

func (a *Adapter) fetchThreadContext(ctx context.Context, channelID, threadTS, currentMessageTS string) string {
	msgs, _, _, err := a.client.GetConversationRepliesContext(ctx, &slack.GetConversationRepliesParameters{
		ChannelID: channelID,
		Timestamp: threadTS,
		Inclusive: true,
	})
	if err != nil {
		slog.Warn("failed to fetch thread context", "channel", channelID, "thread_ts", threadTS, "error", err)
		return ""
	}

	return formatThreadContext(msgs, currentMessageTS)
}

func formatThreadContext(msgs []slack.Message, currentMessageTS string) string {
	var sb strings.Builder
	for _, msg := range msgs {
		if msg.Timestamp == currentMessageTS {
			continue
		}
		if msg.Text == "" {
			continue
		}
		user := msg.User
		if user == "" {
			user = "Bot"
		}
		fmt.Fprintf(&sb, "%s: %s\n", user, msg.Text)
	}
	if sb.Len() == 0 {
		return ""
	}
	return "[Thread context:]\n" + sb.String()
}

func stripBotMention(text, botUserID string) string {
	mention := fmt.Sprintf("<@%s>", botUserID)
	text = strings.Replace(text, mention, "", 1)
	return strings.TrimSpace(text)
}

// Compile-time check that Adapter implements adapter.Adapter.
var _ adapter.Adapter = (*Adapter)(nil)
