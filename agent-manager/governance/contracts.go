// SPDX-License-Identifier: AGPL-3.0-or-later

package governance

import (
	"encoding/json"
	"fmt"

	platformschema "github.com/e6qu/intraktible/platform/schema"
)

func validateContract(name string, contract, document json.RawMessage) error {
	var object map[string]any
	if err := json.Unmarshal(document, &object); err != nil {
		return fmt.Errorf("%s is not a JSON object: %w", name, err)
	}
	if err := platformschema.ValidateObject(contract, object); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}
