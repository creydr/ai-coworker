package github

import (
	"fmt"
	"strconv"

	"github.com/creydr/ai-coworker/internal/domain"
)

// GitHubRef holds the parsed properties of a GitHub channel reference
type GitHubRef struct {
	Repo        string
	IssueNum    int
	CommentID   int64
	CommentType string
}

// NewRef creates a new GitHub channel reference for the given repo and issue number
func NewRef(repo string, issueNum int) domain.ChannelRef {
	return domain.ChannelRef{
		Channel:   "github",
		ThreadKey: fmt.Sprintf("%s#%d", repo, issueNum),
		Properties: map[string]string{
			"repo":      repo,
			"issue_num": strconv.Itoa(issueNum),
		},
	}
}

// WithComment returns a copy of ref with the given comment ID and type attached
func WithComment(ref domain.ChannelRef, commentID int64, commentType string) domain.ChannelRef {
	if ref.Properties == nil {
		ref.Properties = make(map[string]string)
	}
	ref.Properties["comment_id"] = strconv.FormatInt(commentID, 10)
	ref.Properties["comment_type"] = commentType
	return ref
}

// ParseRef extracts a GitHubRef from a generic channel reference
func ParseRef(ref domain.ChannelRef) GitHubRef {
	if ref.Properties == nil {
		return GitHubRef{}
	}

	r := GitHubRef{
		Repo:        ref.Properties["repo"],
		CommentType: ref.Properties["comment_type"],
	}
	if n, err := strconv.Atoi(ref.Properties["issue_num"]); err == nil {
		r.IssueNum = n
	}
	if id, err := strconv.ParseInt(ref.Properties["comment_id"], 10, 64); err == nil {
		r.CommentID = id
	}
	return r
}
