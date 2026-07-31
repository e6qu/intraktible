// SPDX-License-Identifier: AGPL-3.0-or-later

package service

import (
	"bytes"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/e6qu/intraktible/modeling/domain"
	"github.com/e6qu/intraktible/platform/httpx"
)

func (s *Service) exportSnapshot(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	rows, manifest, err := s.ReadSnapshotRows(r.Context(), id, r.PathValue("snapshot_id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	format := r.URL.Query().Get("format")
	if format == "" {
		format = "json"
	}
	var (
		payload     []byte
		contentType string
		extension   string
	)
	switch format {
	case "json":
		hash, encoded, encodeErr := domain.HashRows(rows)
		if encodeErr != nil {
			httpx.Error(w, http.StatusInternalServerError, encodeErr)
			return
		}
		if hash != manifest.RowsHash {
			httpx.Error(w, http.StatusInternalServerError, fmt.Errorf(
				"modeling: exported snapshot hash %s, want %s", hash, manifest.RowsHash,
			))
			return
		}
		payload, contentType, extension = encoded, "application/json", "json"
	case "csv":
		encoded, encodeErr := snapshotCSV(rows)
		if encodeErr != nil {
			httpx.Error(w, http.StatusInternalServerError, encodeErr)
			return
		}
		payload, contentType, extension = encoded, "text/csv; charset=utf-8", "csv"
	default:
		httpx.Error(w, http.StatusBadRequest, fmt.Errorf(
			"modeling: unsupported snapshot export format %q", format,
		))
		return
	}
	contentHash := sha256.Sum256(payload)
	w.Header().Set("X-Intraktible-Content-SHA256", hex.EncodeToString(contentHash[:]))
	httpx.Download(
		w, contentType, safeExportName(manifest.SnapshotID)+"."+extension, string(payload),
	)
}

func snapshotCSV(rows []domain.DatasetRow) ([]byte, error) {
	canonical := append([]domain.DatasetRow(nil), rows...)
	sort.Slice(canonical, func(i, j int) bool {
		return canonical[i].EntityID < canonical[j].EntityID
	})
	featureSet := map[string]bool{}
	segmentSet := map[string]bool{}
	for _, row := range canonical {
		for name := range row.Features {
			featureSet[name] = true
		}
		for name := range row.Segments {
			segmentSet[name] = true
		}
	}
	features := sortedKeys(featureSet)
	segments := sortedKeys(segmentSet)
	header := []string{"entity_id"}
	for _, name := range features {
		header = append(header, "feature:"+name)
	}
	header = append(header, "label", "label_present", "censored")
	for _, name := range segments {
		header = append(header, "segment:"+name)
	}
	header = append(header, "partition", "observation_at", "knowledge_at")

	var buffer bytes.Buffer
	writer := csv.NewWriter(&buffer)
	if err := writer.Write(header); err != nil {
		return nil, err
	}
	for _, row := range canonical {
		record := []string{row.EntityID}
		for _, name := range features {
			record = append(record, strconv.FormatFloat(row.Features[name], 'g', -1, 64))
		}
		record = append(
			record, string(row.Label), strconv.FormatBool(row.LabelPresent),
			strconv.FormatBool(row.Censored),
		)
		for _, name := range segments {
			record = append(record, row.Segments[name])
		}
		record = append(
			record, row.Partition, row.ObservationAt.UTC().Format("2006-01-02T15:04:05.999999999Z"),
			row.KnowledgeAt.UTC().Format("2006-01-02T15:04:05.999999999Z"),
		)
		if err := writer.Write(record); err != nil {
			return nil, err
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

func sortedKeys(values map[string]bool) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func safeExportName(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, "snapshot-"+value)
}
