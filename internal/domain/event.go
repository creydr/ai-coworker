package domain

type IncomingEvent struct {
	ChannelRef ChannelRef
	ThreadID   string
	UserID     string
	Content    string
	Metadata   map[string]string
}

type ChannelRef struct {
	Channel    string
	ThreadKey  string
	Properties map[string]string
}
