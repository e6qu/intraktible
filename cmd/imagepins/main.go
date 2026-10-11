// SPDX-License-Identifier: AGPL-3.0-or-later

// Command imagepins fails when a pinned container image is behind what Docker
// Hub serves for it.
//
// Every image this repository pulls is a copy of a Docker Hub image, pinned by
// tag and digest: public.ecr.aws/docker/library/<name> for official images and
// mirror.gcr.io/<repository> for the rest, because Docker Hub limits anonymous
// pulls and the shared CI runners exhaust that limit. A digest that never moves
// rots, so each pin is compared with Docker Hub:
//
//	ok     the pin is the digest Docker Hub serves for its tag, and the mirror
//	       serves that digest too
//	HELD   Docker Hub has something newer (a new digest for the tag, or a newer
//	       release with the same variant and major version), but it is less
//	       than 24 hours old
//	DRIFT  Docker Hub has had something newer for at least 24 hours, or the
//	       mirror serves a different digest than the pin: fails
//
// ECR Public allows one unauthenticated request per second, so the check takes
// one registry-wide token, spaces its requests to ECR Public 1.1 seconds apart
// and, when ECR Public still answers 429 (other clients behind the same address
// count too), waits as long as its Retry-After asks before trying again, at most
// three times. Run it with `make image-pins`.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const quarantine = 24 * time.Hour

var (
	pinPattern = regexp.MustCompile(
		`(public\.ecr\.aws/docker/library|mirror\.gcr\.io)/([a-z0-9._/-]+):([A-Za-z0-9_.-]+)@(sha256:[0-9a-f]{64})`)
	versionPattern = regexp.MustCompile(`^(v?)(\d+(?:\.\d+)*)(-[A-Za-z0-9_.-]+)?$`)
)

// pin is one pinned image reference.
type pin struct {
	Registry string // public.ecr.aws/docker/library or mirror.gcr.io
	Path     string // the repository path within the registry
	Hub      string // the Docker Hub repository it copies
	Tag      string
	Digest   string
}

// release is a Docker Hub tag and the time it was last pushed.
type release struct {
	Tag    string
	Pushed time.Time
}

// findPins maps every pin in the given file contents to the files naming it.
func findPins(files map[string]string) map[pin][]string {
	found := map[pin][]string{}
	for name, text := range files {
		for _, m := range pinPattern.FindAllStringSubmatch(text, -1) {
			hub := "library/" + m[2]
			if m[1] == "mirror.gcr.io" && strings.Contains(m[2], "/") {
				hub = m[2]
			}
			p := pin{Registry: m[1], Path: m[2], Hub: hub, Tag: m[3], Digest: m[4]}
			if names := found[p]; len(names) == 0 || names[len(names)-1] != name {
				found[p] = append(names, name)
			}
		}
	}
	for p := range found {
		sort.Strings(found[p])
	}
	return found
}

type version struct {
	prefix  string
	numbers []int
	suffix  string
}

func parseVersion(tag string) (version, bool) {
	m := versionPattern.FindStringSubmatch(tag)
	if m == nil {
		return version{}, false
	}
	parts := strings.Split(m[2], ".")
	numbers := make([]int, len(parts))
	for i, part := range parts {
		n, err := strconv.Atoi(part)
		if err != nil {
			return version{}, false
		}
		numbers[i] = n
	}
	return version{prefix: m[1], numbers: numbers, suffix: m[3]}, true
}

func compareNumbers(a, b []int) int {
	for i := range a {
		if a[i] != b[i] {
			return a[i] - b[i]
		}
	}
	return 0
}

// newerReleases returns the candidates that are newer releases of the same
// major version and variant as tag, newest first. Tags that are not versions
// (`latest`, `alpine`) have no newer releases.
func newerReleases(tag string, candidates []release) []release {
	current, ok := parseVersion(tag)
	if !ok {
		return nil
	}
	type ranked struct {
		release
		numbers []int
	}
	var newer []ranked
	for _, c := range candidates {
		v, ok := parseVersion(c.Tag)
		if !ok || v.prefix != current.prefix || v.suffix != current.suffix ||
			len(v.numbers) != len(current.numbers) || v.numbers[0] != current.numbers[0] ||
			compareNumbers(v.numbers, current.numbers) <= 0 {
			continue
		}
		newer = append(newer, ranked{c, v.numbers})
	}
	sort.Slice(newer, func(i, j int) bool { return compareNumbers(newer[i].numbers, newer[j].numbers) > 0 })
	out := make([]release, len(newer))
	for i, r := range newer {
		out[i] = r.release
	}
	return out
}

// verdict classifies one pin as ok, HELD or DRIFT, with the reasons.
func verdict(pinned, hubDigest string, hubPushed time.Time, mirrorDigest string, newer []release, now time.Time) (state string, notes []string) {
	var problems, held []string
	switch {
	case hubDigest != pinned && now.Sub(hubPushed) >= quarantine:
		problems = append(problems, fmt.Sprintf("Docker Hub has served %s for the tag since %s", hubDigest, hubPushed.Format(time.RFC3339)))
	case hubDigest != pinned:
		held = append(held, fmt.Sprintf("Docker Hub moved the tag to %s at %s", hubDigest, hubPushed.Format(time.RFC3339)))
	case mirrorDigest != pinned:
		problems = append(problems, fmt.Sprintf("the mirror serves %s for the tag, not the pinned digest", mirrorDigest))
	}
	for _, r := range newer {
		if now.Sub(r.Pushed) >= quarantine {
			problems = append(problems, fmt.Sprintf("newer release %s has been on Docker Hub since %s", r.Tag, r.Pushed.Format(time.RFC3339)))
			break
		}
	}
	if len(problems) == 0 && len(newer) > 0 {
		held = append(held, fmt.Sprintf("newer release %s is inside the 24-hour quarantine", newer[0].Tag))
	}
	switch {
	case len(problems) > 0:
		return "DRIFT", problems
	case len(held) > 0:
		return "HELD", held
	default:
		return "ok", nil
	}
}

const indexTypes = "application/vnd.oci.image.index.v1+json, " +
	"application/vnd.docker.distribution.manifest.list.v2+json, " +
	"application/vnd.oci.image.manifest.v1+json, " +
	"application/vnd.docker.distribution.manifest.v2+json"

type registries struct {
	client   *http.Client
	lastECR  time.Time
	ecrToken string
}

func (r *registries) do(ctx context.Context, method, rawURL string, header http.Header) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, rawURL, http.NoBody)
	if err != nil {
		return nil, err
	}
	for k, v := range header {
		req.Header[k] = v
	}
	req.Header.Set("User-Agent", "intraktible-imagepins")
	resp, err := r.client.Do(req)
	if err != nil {
		return nil, err
	}
	for attempt := 1; resp.StatusCode == http.StatusTooManyRequests && attempt < 3; attempt++ {
		wait := 2 * time.Second
		if seconds, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && seconds > 0 {
			wait = time.Duration(seconds) * time.Second
		}
		_ = resp.Body.Close()
		time.Sleep(wait)
		if resp, err = r.client.Do(req); err != nil {
			return nil, err
		}
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("%s %s: HTTP %d", method, rawURL, resp.StatusCode)
	}
	return resp, nil
}

func (r *registries) getJSON(ctx context.Context, rawURL string, into any) error {
	resp, err := r.do(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	return json.Unmarshal(body, into)
}

// ecrPause keeps ECR Public requests a little over a second apart.
func (r *registries) ecrPause() {
	if wait := time.Until(r.lastECR.Add(1100 * time.Millisecond)); wait > 0 {
		time.Sleep(wait)
	}
	r.lastECR = time.Now()
}

func (r *registries) mirrorDigest(ctx context.Context, p pin) (string, error) {
	header := http.Header{"Accept": []string{indexTypes}}
	manifest := "https://mirror.gcr.io/v2/" + p.Path + "/manifests/" + p.Tag
	if p.Registry != "mirror.gcr.io" {
		if r.ecrToken == "" {
			var token struct {
				Token string `json:"token"`
			}
			r.ecrPause()
			if err := r.getJSON(ctx, "https://public.ecr.aws/token/?scope=aws&service=public.ecr.aws", &token); err != nil {
				return "", err
			}
			if token.Token == "" {
				return "", errors.New("ECR Public returned an empty token")
			}
			r.ecrToken = token.Token
		}
		header.Set("Authorization", "Bearer "+r.ecrToken)
		manifest = "https://public.ecr.aws/v2/docker/library/" + p.Path + "/manifests/" + p.Tag
		r.ecrPause()
	}
	resp, err := r.do(ctx, http.MethodHead, manifest, header)
	if err != nil {
		return "", err
	}
	_ = resp.Body.Close()
	digest := resp.Header.Get("Docker-Content-Digest")
	if digest == "" {
		return "", fmt.Errorf("%s returned no digest for %s:%s", p.Registry, p.Path, p.Tag)
	}
	return digest, nil
}

type hubTag struct {
	Name          string `json:"name"`
	Digest        string `json:"digest"`
	TagLastPushed string `json:"tag_last_pushed"`
}

func (r *registries) hubTag(ctx context.Context, p pin) (digest string, pushed time.Time, err error) {
	var tag hubTag
	if err := r.getJSON(ctx, "https://hub.docker.com/v2/repositories/"+p.Hub+"/tags/"+url.PathEscape(p.Tag), &tag); err != nil {
		return "", time.Time{}, err
	}
	pushed, err = time.Parse(time.RFC3339Nano, tag.TagLastPushed)
	if err != nil || tag.Digest == "" {
		return "", time.Time{}, fmt.Errorf("docker hub returned no digest or push time for %s:%s", p.Hub, p.Tag)
	}
	return tag.Digest, pushed, nil
}

func (r *registries) hubCandidates(ctx context.Context, p pin) ([]release, error) {
	v, ok := parseVersion(p.Tag)
	if !ok {
		return nil, nil
	}
	var page struct {
		Results []hubTag `json:"results"`
	}
	name := url.QueryEscape(v.prefix + strconv.Itoa(v.numbers[0]) + ".")
	if err := r.getJSON(ctx, "https://hub.docker.com/v2/repositories/"+p.Hub+"/tags?page_size=100&ordering=last_updated&name="+name, &page); err != nil {
		return nil, err
	}
	var out []release
	for _, t := range page.Results {
		pushed, err := time.Parse(time.RFC3339Nano, t.TagLastPushed)
		if err != nil {
			continue // a tag Docker Hub lists without a push time has never been pushed
		}
		out = append(out, release{Tag: t.Name, Pushed: pushed})
	}
	return out, nil
}

func trackedFiles() (map[string]string, error) {
	out, err := exec.Command("git", "grep", "-lE",
		`(public\.ecr\.aws/docker/library|mirror\.gcr\.io)/[a-z0-9._/-]+:[A-Za-z0-9_.-]+@sha256:`,
		"--", ":!*.md").Output()
	var exit *exec.ExitError
	if err != nil && (!errors.As(err, &exit) || exit.ExitCode() != 1) {
		return nil, fmt.Errorf("git grep: %w", err)
	}
	files := map[string]string{}
	for _, name := range strings.Fields(string(out)) {
		text, err := os.ReadFile(name) // #nosec G304 -- names come from git's own file list
		if err != nil {
			return nil, err
		}
		files[name] = string(text)
	}
	return files, nil
}

func run(ctx context.Context, stdout, stderr io.Writer) int {
	files, err := trackedFiles()
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	found := findPins(files)
	if len(found) == 0 {
		_, _ = fmt.Fprintln(stderr, "found no pinned images; the check is not looking at anything")
		return 1
	}
	ordered := make([]pin, 0, len(found))
	for p := range found {
		ordered = append(ordered, p)
	}
	sort.Slice(ordered, func(i, j int) bool {
		return ordered[i].Registry+ordered[i].Path+":"+ordered[i].Tag+ordered[i].Digest <
			ordered[j].Registry+ordered[j].Path+":"+ordered[j].Tag+ordered[j].Digest
	})
	r := &registries{client: &http.Client{Timeout: 20 * time.Second}}
	now := time.Now()
	failed := 0
	for _, p := range ordered {
		label := fmt.Sprintf("%s/%s:%s (%s)", p.Registry, p.Path, p.Tag, strings.Join(found[p], ", "))
		state, notes, err := check(ctx, r, p, now)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "ERROR  %s: could not be checked: %v\n", label, err)
			failed++
			continue
		}
		_, _ = fmt.Fprintf(stdout, "%-6s %s\n", state, label)
		for _, note := range notes {
			_, _ = fmt.Fprintf(stdout, "         %s\n", note)
		}
		if state == "DRIFT" {
			failed++
		}
	}
	if failed > 0 {
		_, _ = fmt.Fprintf(stderr, "%d image pin(s) are behind Docker Hub or could not be checked. Bump each one to the "+
			"digest Docker Hub serves once the mirror serves the same digest.\n", failed)
		return 1
	}
	_, _ = fmt.Fprintf(stdout, "All %d pinned images are current or held.\n", len(found))
	return 0
}

func check(ctx context.Context, r *registries, p pin, now time.Time) (state string, notes []string, err error) {
	hubDigest, hubPushed, err := r.hubTag(ctx, p)
	if err != nil {
		return "", nil, err
	}
	mirrored, err := r.mirrorDigest(ctx, p)
	if err != nil {
		return "", nil, err
	}
	candidates, err := r.hubCandidates(ctx, p)
	if err != nil {
		return "", nil, err
	}
	state, notes = verdict(p.Digest, hubDigest, hubPushed, mirrored, newerReleases(p.Tag, candidates), now)
	return state, notes, nil
}

func main() {
	os.Exit(run(context.Background(), os.Stdout, os.Stderr))
}
