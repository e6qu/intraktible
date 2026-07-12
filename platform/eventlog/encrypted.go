// SPDX-License-Identifier: AGPL-3.0-or-later

package eventlog

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/e6qu/intraktible/platform/secretbox"
)

// Encrypted wraps a Log so event PAYLOADS are sealed at rest under the keyring
// (AES-256-GCM via platform/secretbox). Only the payload is sealed; the routing and
// ordering metadata (Org/Workspace/Stream/Type/Time/Actor/Seq) and the optimistic-
// concurrency Unique claim stay plaintext, so total ordering, the uniqueness
// constraint, and every audit metadata filter keep working. The sealed payload is a
// JSON envelope (valid JSON), so the WAL/NATS whole-envelope encoding stays well
// formed. Reads, replay, and the in-process bus transparently open the payload — and
// pass any plaintext payload (appended before encryption was enabled) straight
// through — so enabling encryption needs no migration. A nil keyring returns the log
// unwrapped (encryption disabled).
func Encrypted(inner Log, kr *secretbox.Keyring) Log {
	if kr == nil {
		return inner
	}
	return &encLog{inner: inner, kr: kr}
}

type encLog struct {
	inner Log
	kr    *secretbox.Keyring
}

// undecodablePayload is deliberately invalid JSON, substituted for a payload that
// failed to decrypt on the live bus so the owning projector errors loudly (rather
// than decoding a sealed envelope into a zero-value struct).
var undecodablePayload = []byte("\x00eventlog: payload decryption failed")

// open returns the plaintext payload: a sealed envelope is opened, a plaintext
// payload (or an empty one) passes through.
func (l *encLog) open(p json.RawMessage) (json.RawMessage, error) {
	if len(p) == 0 || !secretbox.IsSealed(p) {
		return p, nil
	}
	return l.kr.Open(p, nil)
}

func (l *encLog) Append(ctx context.Context, e Envelope) (Envelope, error) {
	plain := e.Payload
	if len(e.Payload) > 0 {
		sealed, err := l.kr.Seal(e.Payload, nil)
		if err != nil {
			return Envelope{}, err
		}
		e.Payload = sealed
	}
	stored, err := l.inner.Append(ctx, e)
	if err != nil {
		return stored, err
	}
	// Hand the caller back the plaintext it gave us (with the assigned Seq/ID); only
	// what lands in storage is sealed.
	stored.Payload = plain
	return stored, nil
}

func (l *encLog) Read(ctx context.Context, fromSeq uint64) ([]Envelope, error) {
	return l.decryptAll(l.inner.Read(ctx, fromSeq))
}

// ReadTenantStream delegates the (possibly indexed) filtered read to the inner log,
// then decrypts — so the encryption wrapper keeps the inner backend's index.
func (l *encLog) ReadTenantStream(ctx context.Context, org, workspace, stream string, fromSeq uint64) ([]Envelope, error) {
	return l.decryptAll(l.inner.ReadTenantStream(ctx, org, workspace, stream, fromSeq))
}

// decryptAll opens every event's sealed payload in place.
func (l *encLog) decryptAll(evs []Envelope, err error) ([]Envelope, error) {
	if err != nil {
		return nil, err
	}
	for i := range evs {
		p, err := l.open(evs[i].Payload)
		if err != nil {
			return nil, err
		}
		evs[i].Payload = p
	}
	return evs, nil
}

func (l *encLog) Subscribe() (<-chan Envelope, func()) {
	in, cancel := l.inner.Subscribe()
	out := make(chan Envelope)
	go func() {
		defer close(out)
		for e := range in {
			p, err := l.open(e.Payload)
			if err != nil {
				// A payload that won't open (a missing/rotated-away key) is an operator
				// misconfiguration. Replace it with a NON-DECODABLE sentinel rather than
				// forwarding the sealed envelope: a sealed envelope is valid JSON, so a
				// projector would unmarshal it into a zero-value struct (no error) and
				// the runtime would advance its checkpoint — silently writing corrupt
				// docs. The sentinel makes the owning projector's decode fail, so the
				// runtime halts and /healthz reports degraded (the intended loud failure).
				slog.Error("eventlog: decrypt payload on delivery", "seq", e.Seq, "stream", e.Stream, "err", err)
				e.Payload = undecodablePayload
			} else {
				e.Payload = p
			}
			out <- e
		}
	}()
	return out, cancel
}

func (l *encLog) Head() uint64 { return l.inner.Head() }
func (l *encLog) Close() error { return l.inner.Close() }
