// SPDX-License-Identifier: AGPL-3.0-or-later

package privacy

// StreamPrivacy is the event stream for the masking configuration.
const StreamPrivacy = "platform.privacy"

// TypeFieldsSet records a replacement of a workspace's sensitive-field list.
const TypeFieldsSet = "platform.privacy_fields_set"

// FieldsSet is the (whole) set of sensitive field names for a workspace.
type FieldsSet struct {
	Fields []string `json:"fields"`
}
