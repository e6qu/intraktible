// SPDX-License-Identifier: AGPL-3.0-or-later

package service

import (
	"context"
	"fmt"

	"github.com/e6qu/intraktible/context-layer/features"
	"github.com/e6qu/intraktible/modeling/domain"
	modelprojection "github.com/e6qu/intraktible/modeling/projection"
	"github.com/e6qu/intraktible/platform/identity"
)

// schemaDependents returns the datasets that still reference a schema version,
// so retirement can refuse to strand a governed dependant. An entity schema is
// referenced by every dataset over that entity type; an event schema is
// referenced by any dataset whose label or features read that event.
func (s *Service) schemaDependents(
	ctx context.Context,
	id identity.Identity,
	ref domain.SchemaRef,
) ([]string, error) {
	datasets, err := modelprojection.ListDatasets(ctx, s.store, id)
	if err != nil {
		return nil, err
	}
	definitions, err := features.List(ctx, s.store, id, "")
	if err != nil {
		return nil, err
	}
	eventByFeature := make(map[string]string)
	for _, definition := range definitions {
		key := definition.EntityType + "\x00" + definition.Name
		eventByFeature[key] = definition.EventName
	}
	var dependants []string
	for _, dataset := range datasets {
		if datasetReferencesSchema(dataset, ref, eventByFeature) {
			dependants = append(dependants, dataset.Name)
		}
	}
	return dependants, nil
}

func datasetReferencesSchema(
	dataset modelprojection.DatasetView,
	ref domain.SchemaRef,
	eventByFeature map[string]string,
) bool {
	for _, version := range dataset.Versions {
		spec := version.Spec
		if ref.EventName == "" {
			if spec.EntityType == ref.EntityType {
				return true
			}
			continue
		}
		if spec.Label.EventName == ref.EventName && spec.EntityType == ref.EntityType {
			return true
		}
		for _, feature := range spec.Features {
			if eventByFeature[spec.EntityType+"\x00"+feature] == ref.EventName {
				return true
			}
		}
	}
	return false
}

func dependantsError(dependants []string) error {
	return fmt.Errorf(
		"modeling: %d dataset(s) still depend on this contract (%v); retire or migrate them first",
		len(dependants), dependants,
	)
}
