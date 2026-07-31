// SPDX-License-Identifier: AGPL-3.0-or-later

package httpx

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPaginateSlicesAndCarriesCursor(t *testing.T) {
	t.Parallel()
	items := []int{1, 2, 3, 4, 5, 6, 7}

	r := httptest.NewRequest("GET", "/x?limit=3", http.NoBody)
	page, err := Paginate(r, items)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 3 || page.Items[0] != 1 || page.NextCursor != "3" {
		t.Fatalf("first page = %+v, want [1 2 3] cursor 3", page)
	}

	r = httptest.NewRequest("GET", "/x?limit=3&cursor=3", http.NoBody)
	page, err = Paginate(r, items)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 3 || page.Items[0] != 4 || page.NextCursor != "6" {
		t.Fatalf("second page = %+v, want [4 5 6] cursor 6", page)
	}

	r = httptest.NewRequest("GET", "/x?limit=3&cursor=6", http.NoBody)
	page, err = Paginate(r, items)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0] != 7 || page.NextCursor != "" {
		t.Fatalf("last page = %+v, want [7] no cursor", page)
	}
}

func TestPaginateDefaultsToWholeList(t *testing.T) {
	t.Parallel()
	r := httptest.NewRequest("GET", "/x", http.NoBody)
	page, err := Paginate(r, []int{1, 2, 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 3 || page.NextCursor != "" {
		t.Fatalf("no params = %+v, want whole list", page)
	}
}

func TestPaginateRejectsInvalidParams(t *testing.T) {
	t.Parallel()
	for _, url := range []string{"/x?limit=0", "/x?limit=99999", "/x?cursor=-1", "/x?cursor=abc", "/x?limit=abc"} {
		r := httptest.NewRequest("GET", url, http.NoBody)
		if _, err := Paginate(r, []int{1}); err == nil {
			t.Fatalf("%s should be rejected", url)
		}
	}
}
