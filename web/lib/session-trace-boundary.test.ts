import { existsSync, readFileSync } from 'node:fs';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import ts from 'typescript';
import { expect, it } from 'vitest';
import { shouldPersistWeeklyQuery } from './weekly-query';
import { QueryClient } from '@tanstack/react-query';

const root = fileURLToPath(new URL('..', import.meta.url));
const staticDependencies = (entries: string[]): Set<string> => {
  const seen = new Set<string>(), pending = entries.map(path => resolve(root, path));
  while (pending.length) {
    const file = pending.pop()!;
    if (seen.has(file)) continue;
    seen.add(file);
    const source = ts.createSourceFile(file, readFileSync(file, 'utf8'), ts.ScriptTarget.Latest, true);
    for (const statement of source.statements) {
      if (!ts.isImportDeclaration(statement) || !ts.isStringLiteral(statement.moduleSpecifier) || statement.importClause?.isTypeOnly) continue;
      const imports = statement.importClause?.namedBindings;
      if (!statement.importClause?.name && imports && ts.isNamedImports(imports) && imports.elements.every(element => element.isTypeOnly)) continue;
      const specifier = statement.moduleSpecifier.text;
      if (!specifier.startsWith('.') && !specifier.startsWith('@/')) { seen.add(specifier); continue; }
      const base = specifier.startsWith('@/') ? resolve(root, specifier.slice(2)) : resolve(dirname(file), specifier);
      const target = [base, `${base}.ts`, `${base}.tsx`, resolve(base, 'index.ts'), resolve(base, 'index.tsx')].find(candidate => existsSync(candidate) && /\.(ts|tsx)$/.test(candidate));
      if (target) pending.push(target);
    }
  }
  return seen;
};
it('keeps the basic viewer/layout static graph outside trace/chart/analysis rendering', () => {
  const viewer = [...staticDependencies(['components/sessions/session-viewer.tsx', 'app/(dashboard)/layout.tsx'])];
  expect(viewer).not.toContain('recharts');
  const graph = [...staticDependencies(['components/sessions/session-viewer.tsx', 'app/(dashboard)/layout.tsx', 'app/(dashboard)/sessions/page.tsx'])];
  // The pre-existing optional project trend is separate from the new detail renderer.
  expect(graph.filter(file => /sessions\/trace\/|session-trace-(model|api)|use-session-trace|turn-context|analysis\/analysis-tab/.test(file))).toEqual([]);
});
it('never persists trace snapshots or conversation context to browser storage', () => {
  const client = new QueryClient();
  client.setQueryData(['session-trace', 7, 'context', 'snapshot'], { records: [] });
  client.setQueryData(['session-turn-context', 7, 'user', 'session', 'turn', 'revision'], { records: [] });
  for (const query of client.getQueryCache().getAll()) expect(shouldPersistWeeklyQuery(query)).toBe(false);
});
