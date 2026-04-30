package domain

type IncomingEvent struct {
	Channel    string
	ChannelRef ChannelRef
	ThreadID   string
	UserID     string
	Content    string
	Metadata   map[string]string
}

type ChannelRef struct {
	Channel   string
	ChannelID string
	ThreadTS  string
	Repo      string
	IssueNum  int
	CommentID int64
}
