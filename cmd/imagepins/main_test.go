// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

var (
	now    = time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	old    = now.Add(-quarantine)
	young  = old.Add(time.Microsecond)
	pinned = "sha256:" + strings.Repeat("a", 64)
	moved  = "sha256:" + strings.Repeat("b", 64)
)

func TestPinsNameTheDockerHubRepositoryTheyCopy(t *testing.T) {
	found := findPins(map[string]string{
		"Dockerfile": "FROM public.ecr.aws/docker/library/golang:1.26-bookworm@" + pinned + " AS build",
		"ci.yml":     "driver-opts: image=mirror.gcr.io/moby/buildkit:buildx-stable-1@" + pinned,
		"other.yml":  "image: public.ecr.aws/docker/library/postgres:17-alpine",
	})
	want := map[pin][]string{
		{Registry: "public.ecr.aws/docker/library", Path: "golang", Hub: "library/golang", Tag: "1.26-bookworm", Digest: pinned}: {"Dockerfile"},
		{Registry: "mirror.gcr.io", Path: "moby/buildkit", Hub: "moby/buildkit", Tag: "buildx-stable-1", Digest: pinned}:         {"ci.yml"},
	}
	if !reflect.DeepEqual(found, want) {
		t.Fatalf("findPins = %v, want %v", found, want)
	}
}

func TestNewerReleasesKeepTheVariantAndMajorVersion(t *testing.T) {
	candidates := []release{
		{"17.12-alpine", old}, {"17.12", old}, {"18.1-alpine", old}, {"17.6-alpine", old},
		{"17-alpine", old}, {"17.4-alpine", old}, {"17.12.1-alpine", old},
	}
	var got []string
	for _, r := range newerReleases("17.5-alpine", candidates) {
		got = append(got, r.Tag)
	}
	if want := []string{"17.12-alpine", "17.6-alpine"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("newerReleases = %v, want %v", got, want)
	}
	if r := newerReleases("buildx-stable-1", candidates); r != nil {
		t.Fatalf("a tag that is not a version has no newer releases, got %v", r)
	}
	if r := newerReleases("v0.33.0", []release{{"v0.34.0", old}, {"0.35.0", old}}); len(r) != 1 || r[0].Tag != "v0.34.0" {
		t.Fatalf("the v prefix must match, got %v", r)
	}
}

func TestVerdict(t *testing.T) {
	cases := []struct {
		name      string
		hubDigest string
		hubPushed time.Time
		mirror    string
		newer     []release
		want      string
	}{
		{"current pin served by the mirror", pinned, old, pinned, nil, "ok"},
		{"tag moved inside the quarantine", moved, young, pinned, nil, "HELD"},
		{"tag moved 24 hours ago", moved, old, pinned, nil, "DRIFT"},
		{"newer release inside the quarantine", pinned, old, pinned, []release{{"17.12-alpine", young}}, "HELD"},
		{"newer release 24 hours old", pinned, old, pinned, []release{{"17.12-alpine", old}}, "DRIFT"},
		{"mirror serves another digest", pinned, old, moved, nil, "DRIFT"},
	}
	for _, c := range cases {
		if got, notes := verdict(pinned, c.hubDigest, c.hubPushed, c.mirror, c.newer, now); got != c.want {
			t.Errorf("%s: verdict = %s %v, want %s", c.name, got, notes, c.want)
		}
	}
}
