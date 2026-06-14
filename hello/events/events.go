// SPDX-License-Identifier: AGPL-3.0-or-later

// Package events defines the hello feature's event payloads. The hello feature
// is a vertical slice proving command -> event -> projection -> API -> UI.
package events

// Stream is the hello feature's event stream name.
const Stream = "hello"

// TypeHelloRecorded is emitted when a greeting is recorded.
const TypeHelloRecorded = "hello.recorded"

// HelloRecorded is the payload of a TypeHelloRecorded event.
type HelloRecorded struct {
	Name string `json:"name"`
}
