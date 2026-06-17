// SPDX-License-Identifier: AGPL-3.0-or-later

// Package comments is a general commenting-thread capability: a chronological
// thread of comments attached to any subject (subject_type + subject_id), so the
// workflow surfaces that get approved / rejected / promoted carry a discussion and
// an explanation trail. Comments are events, so a thread is durable and auditable.
package comments

// StreamComments is the event stream for comment posts.
const StreamComments = "platform.comments"

// TypeCommentPosted records a new comment on a subject's thread.
const TypeCommentPosted = "platform.comment_posted"

// CommentPosted is one comment appended to a subject's thread. ParentID, when
// set, makes it a reply to another comment (one level of threading).
type CommentPosted struct {
	CommentID   string `json:"comment_id"`
	SubjectType string `json:"subject_type"`
	SubjectID   string `json:"subject_id"`
	Body        string `json:"body"`
	ParentID    string `json:"parent_id,omitempty"`
}
