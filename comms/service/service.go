// SPDX-License-Identifier: AGPL-3.0-or-later

package service

import (
	"context"
	"net/http"

	"github.com/e6qu/intraktible/comms/command"
	"github.com/e6qu/intraktible/comms/domain"
	"github.com/e6qu/intraktible/comms/projection"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/httpx"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

type Service struct {
	cmd   *command.Handler
	store store.Store
}

func New(cmd *command.Handler, st store.Store) *Service {
	return &Service{cmd: cmd, store: st}
}

func (s *Service) Routes(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/comms/channels", s.create)
	mux.HandleFunc("GET /v1/comms/channels", s.list)
	mux.HandleFunc("GET /v1/comms/channels/{name}", s.get)
	mux.HandleFunc("POST /v1/comms/channels/{name}/update", s.update)
	mux.HandleFunc("POST /v1/comms/channels/{name}/pause", s.pause)
	mux.HandleFunc("POST /v1/comms/channels/{name}/resume", s.resume)
	mux.HandleFunc("POST /v1/comms/channels/{name}/retire", s.retire)
	mux.HandleFunc("POST /v1/comms/channels/{name}/deliver", s.deliver)
}

func (s *Service) create(w http.ResponseWriter, r *http.Request) {
	var channel domain.Channel
	httpx.Emit(w, r, &channel, func(id identity.Identity) (eventlog.Envelope, error) {
		return s.cmd.CreateChannel(r.Context(), id, channel)
	})
}

func (s *Service) list(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	items, err := listChannels(r.Context(), s.store, id)
	httpx.WriteList(w, "channels", items, err)
}

func (s *Service) get(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	view, found, err := readChannel(r.Context(), s.store, id, r.PathValue("name"))
	httpx.WriteOne(w, view, found, err, "comms: channel not found")
}

func (s *Service) update(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Config map[string]any `json:"config"`
	}
	httpx.Emit(w, r, &request, func(id identity.Identity) (eventlog.Envelope, error) {
		return s.cmd.UpdateChannel(r.Context(), id, r.PathValue("name"), request.Config)
	})
}

func (s *Service) pause(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Reason string `json:"reason"`
	}
	httpx.Emit(w, r, &request, func(id identity.Identity) (eventlog.Envelope, error) {
		return s.cmd.PauseChannel(r.Context(), id, r.PathValue("name"), request.Reason)
	})
}

func (s *Service) resume(w http.ResponseWriter, r *http.Request) {
	httpx.Act(w, r, func(id identity.Identity) (eventlog.Envelope, error) {
		return s.cmd.ResumeChannel(r.Context(), id, r.PathValue("name"))
	})
}

func (s *Service) retire(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Reason string `json:"reason"`
	}
	httpx.Emit(w, r, &request, func(id identity.Identity) (eventlog.Envelope, error) {
		return s.cmd.RetireChannel(r.Context(), id, r.PathValue("name"), request.Reason)
	})
}

type deliverRequest struct {
	Evidence domain.DeliveryEvidence `json:"evidence"`
}

func (s *Service) deliver(w http.ResponseWriter, r *http.Request) {
	var request deliverRequest
	httpx.Emit(w, r, &request, func(id identity.Identity) (eventlog.Envelope, error) {
		return s.cmd.RecordDelivery(r.Context(), id, r.PathValue("name"), request.Evidence)
	})
}

func listChannels(
	ctx context.Context,
	st store.Store,
	id identity.Identity,
) ([]projection.ChannelView, error) {
	return store.QueryDocs(ctx, st, projection.CollectionChannels,
		store.Key(id.Org, id.Workspace, ""),
		nil,
		func(a, b projection.ChannelView) bool { return a.Name < b.Name })
}

func readChannel(
	ctx context.Context,
	st store.Store,
	id identity.Identity,
	name string,
) (projection.ChannelView, bool, error) {
	return store.GetDoc[projection.ChannelView](
		ctx, st, projection.CollectionChannels,
		store.Key(id.Org, id.Workspace, name))
}
