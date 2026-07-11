// SPDX-License-Identifier: AGPL-3.0-or-later
// Guards the starter templates' caller-input contract: every template ships an
// input_schema (so "Sample input" and decide-time validation work on flows created
// from the gallery), and its required list only names declared properties.
//
// It also guards their graphs against the engine's publish gate (domain.ValidateGraph).
// Templates are imported through POST /v1/flows/import, so a template that violates it
// is a "New from template" button that 400s — which is how three of them shipped with
// splits that had no condition and expression-shaped branch labels, a model the engine
// has never had. Asserting the rules here catches that without booting wasm.

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

describe('TEMPLATES graphs pass the publish gate', () => {
  it.each(TEMPLATES.map((t) => [t.id, t] as const))('%s has one input and an output', (_id, t) => {
    const { nodes } = t.doc.graph;
    expect(nodes.filter((n) => n.type === 'input')).toHaveLength(1);
    expect(nodes.filter((n) => n.type === 'output').length).toBeGreaterThan(0);
  });

  it.each(TEMPLATES.map((t) => [t.id, t] as const))(
    '%s wires every edge to a declared node, with no dead ends',
    (_id, t) => {
      const { nodes, edges } = t.doc.graph;
      const ids = new Set(nodes.map((n) => n.id));
      for (const e of edges) {
        expect(ids, `edge from unknown node "${e.from}"`).toContain(e.from);
        expect(ids, `edge to unknown node "${e.to}"`).toContain(e.to);
      }
      const hasOutgoing = new Set(edges.map((e) => e.from));
      for (const n of nodes.filter((n) => n.type !== 'output')) {
        expect(hasOutgoing, `node "${n.id}" dead-ends`).toContain(n.id);
      }
    }
  );

  it.each(TEMPLATES.map((t) => [t.id, t] as const))(
    '%s reaches every node from the input',
    (_id, t) => {
      const { nodes, edges } = t.doc.graph;
      const input = nodes.find((n) => n.type === 'input');
      if (!input) throw new Error('a template must have an input node');
      const reached = new Set<string>([input.id]);
      const queue = [input.id];
      while (queue.length) {
        const cur = queue.shift();
        for (const e of edges.filter((e) => e.from === cur)) {
          if (!reached.has(e.to)) {
            reached.add(e.to);
            queue.push(e.to);
          }
        }
      }
      for (const n of nodes) {
        expect(reached, `node "${n.id}" is unreachable`).toContain(n.id);
      }
    }
  );

  // A split evaluates one boolean and follows the edge labelled with the answer, so
  // it needs a condition and both edges. It is not a multi-branch "first condition
  // that is true" node, however inviting the branch label makes that look.
  it.each(TEMPLATES.map((t) => [t.id, t] as const))(
    '%s gives every split a condition and both branch edges',
    (_id, t) => {
      const { nodes, edges } = t.doc.graph;
      for (const split of nodes.filter((n) => n.type === 'split')) {
        expect(split.config?.condition, `split "${split.id}" has no condition`).toBeTruthy();
        const branches = edges.filter((e) => e.from === split.id).map((e) => e.branch);
        expect(branches, `split "${split.id}" is missing a branch`).toEqual(
          expect.arrayContaining(['yes', 'no'])
        );
      }
      // Only splits route on a branch label; on any other node it is silently ignored.
      const splitIds = new Set(nodes.filter((n) => n.type === 'split').map((n) => n.id));
      for (const e of edges.filter((e) => e.branch)) {
        expect(splitIds, `edge ${e.from} → ${e.to} labels a branch on a non-split`).toContain(
          e.from
        );
      }
    }
  );
});
