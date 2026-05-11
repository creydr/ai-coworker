package googledocs

import (
	"fmt"

	"github.com/creydr/ai-coworker/internal/domain"
)

type GoogleDocsRef struct {
	DocumentID string
	CommentID  string
}

func NewRef(documentID, commentID string) domain.ChannelRef {
	return domain.ChannelRef{
		Channel:   "googledocs",
		ThreadKey: fmt.Sprintf("%s#%s", documentID, commentID),
		Properties: map[string]string{
			"document_id": documentID,
			"comment_id":  commentID,
		},
	}
}

func ParseRef(ref domain.ChannelRef) GoogleDocsRef {
	if ref.Properties == nil {
		return GoogleDocsRef{}
	}
	return GoogleDocsRef{
		DocumentID: ref.Properties["document_id"],
		CommentID:  ref.Properties["comment_id"],
	}
}
