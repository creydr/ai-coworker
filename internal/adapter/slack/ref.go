package slack

import (
	"fmt"

	"github.com/creydr/ai-coworker/internal/domain"
)

type SlackRef struct {
	ChannelID string
	ThreadTS  string
}

func NewRef(channelID, threadTS string) domain.ChannelRef {
	return domain.ChannelRef{
		Channel:   "slack",
		ThreadKey: fmt.Sprintf("%s/%s", channelID, threadTS),
		Properties: map[string]string{
			"channel_id": channelID,
			"thread_ts":  threadTS,
		},
	}
}

func ParseRef(ref domain.ChannelRef) SlackRef {
	if ref.Properties == nil {
		return SlackRef{}
	}

	return SlackRef{
		ChannelID: ref.Properties["channel_id"],
		ThreadTS:  ref.Properties["thread_ts"],
	}
}
