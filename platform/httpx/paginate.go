// SPDX-License-Identifier: AGPL-3.0-or-later

package httpx

import (
	"net/http"
	"strconv"
)

// MaxPageSize bounds any single paginated response.
const MaxPageSize = 1000

// Page is one cursor-paginated slice of a list endpoint.
type Page[T any] struct {
	Items      []T    `json:"items"`
	NextCursor string `json:"next_cursor,omitempty"`
}

// Paginate slices items by the request's limit/cursor query parameters. The
// cursor is the zero-based offset of the next page (opaque to clients; stable
// across pages because the underlying list is deterministically ordered). limit
// defaults to 0 (the whole list) and is bounded at MaxPageSize. It returns the
// page slice and the next cursor ("" when this is the last page).
func Paginate[T any](r *http.Request, items []T) (Page[T], error) {
	query := r.URL.Query()
	offset := 0
	if cursor := query.Get("cursor"); cursor != "" {
		parsed, err := strconv.Atoi(cursor)
		if err != nil || parsed < 0 {
			return Page[T]{}, &apiError{msg: "invalid cursor (want a non-negative integer offset)"}
		}
		offset = parsed
	}
	limit := 0
	if raw := query.Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > MaxPageSize {
			return Page[T]{}, &apiError{
				msg: "invalid limit (want 1–" + strconv.Itoa(MaxPageSize) + ")",
			}
		}
		limit = parsed
	}
	if offset > len(items) {
		offset = len(items)
	}
	end := len(items)
	next := ""
	if limit > 0 && offset+limit < len(items) {
		end = offset + limit
		next = strconv.Itoa(end)
	}
	return Page[T]{Items: items[offset:end], NextCursor: next}, nil
}

// WritePage responds with one cursor-paginated list, mapping a store error to
// 500 and a pagination error to 400. The response key holds the page items and
// next_cursor carries the continuation token when more pages remain.
func WritePage[T any](w http.ResponseWriter, r *http.Request, key string, items []T, err error) {
	if err != nil {
		Error(w, http.StatusInternalServerError, err)
		return
	}
	page, perr := Paginate(r, items)
	if perr != nil {
		Error(w, http.StatusBadRequest, perr)
		return
	}
	JSON(w, http.StatusOK, map[string]any{key: page.Items, "next_cursor": page.NextCursor})
}

type apiError struct{ msg string }

func (e *apiError) Error() string { return e.msg }
