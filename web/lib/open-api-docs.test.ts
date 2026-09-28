import { describe, expect, it } from 'vitest';
import { OPEN_API_ENDPOINTS, OPEN_API_MANUAL } from './open-api-docs';

describe('Open API manual content', () => {
  it('covers every implemented v1 endpoint', () => {
    expect(OPEN_API_ENDPOINTS.map((endpoint) => endpoint.path)).toEqual([
      '/api/open/v1/sessions',
      '/api/open/v1/sessions/{session_id}',
      '/api/open/v1/tools',
      '/api/open/v1/tools/{tool_name}',
      '/api/open/v1/plugins',
      '/api/open/v1/skills',
      '/api/open/v1/rules',
      '/api/open/v1/rules/{rule_id}',
      '/api/open/v1/events',
      '/api/open/v1/metrics',
      '/api/open/v1/usage',
    ]);
  });

  it('keeps English and Korean manuals structurally aligned', () => {
    expect(OPEN_API_MANUAL.en.sections.map((section) => section.id)).toEqual(
      OPEN_API_MANUAL.ko.sections.map((section) => section.id),
    );
    expect(OPEN_API_MANUAL.en.sections.map((section) => section.id)).toEqual([
      'getting-started',
      'authentication',
      'usage',
      'reference',
      'errors',
      'security',
      'troubleshooting',
    ]);
  });

  it('documents token issuance and safe extraction in both languages', () => {
    for (const locale of ['en', 'ko'] as const) {
      const serialized = JSON.stringify(OPEN_API_MANUAL[locale]);
      expect(serialized).toContain('cctrace auth read');
      expect(serialized).toContain('/api/cli/read-token');
      expect(serialized).toContain('server.read_token');
      expect(serialized).toContain('Authorization: Bearer');
    }
  });
});
