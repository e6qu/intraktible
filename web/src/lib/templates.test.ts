// SPDX-License-Identifier: AGPL-3.0-or-later
// Guards the starter templates' caller-input contract: every template ships an
// input_schema (so "Sample input" and decide-time validation work on flows created
// from the gallery), and its required list only names declared properties.

import { describe, it, expect } from 'vitest';
import { TEMPLATES } from './templates';

describe('TEMPLATES input_schema', () => {
  it.each(TEMPLATES.map((t) => [t.id, t] as const))(
    '%s declares a non-empty object schema',
    (_id, t) => {
      const schema = t.doc.input_schema;
      expect(schema).toBeDefined();
      expect(schema?.type).toBe('object');
      expect(Object.keys(schema?.properties ?? {}).length).toBeGreaterThan(0);
    }
  );

  it.each(TEMPLATES.map((t) => [t.id, t] as const))(
    '%s requires only declared properties',
    (_id, t) => {
      const schema = t.doc.input_schema;
      for (const key of schema?.required ?? []) {
        expect(schema?.properties, `required "${key}" missing from properties`).toHaveProperty(key);
      }
    }
  );
});
