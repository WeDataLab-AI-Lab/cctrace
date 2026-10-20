import { describe, expect, it } from 'vitest';
import { sessionAnalysisCompatibilityUrl } from './session-detail-navigation';

describe('analysis compatibility navigation', () => {
  it('preserves selection and account scope and separates trace range from conversation anchors', () => {
    const url = sessionAnalysisCompatibilityUrl(new URLSearchParams('session_id=A&login_email=a%40test&profile_email=p%40test&user_id=u&turn_key=T&event_id=E&from=start&to=end'));
    const query = new URL(url, 'https://local.test').searchParams;
    expect(url.split('?')[0]).toBe('/sessions');
    expect(Object.fromEntries(query)).toEqual({ session_id: 'A', login_email: 'a@test', profile_email: 'p@test', user_id: 'u', turn_key: 'T', event_id: 'E', trace_from: 'start', trace_to: 'end', detail: 'analysis', expanded: '1' });
    expect(query.has('session_search')).toBe(false);
  });
  it('retains an explicit trace interval and makes an empty legacy route usable', () => {
    expect(sessionAnalysisCompatibilityUrl(new URLSearchParams())).toBe('/sessions?detail=analysis&expanded=1');
    const query = new URL(sessionAnalysisCompatibilityUrl(new URLSearchParams('trace_from=explicit&from=old')), 'https://local.test').searchParams;
    expect(query.get('trace_from')).toBe('explicit');
    expect(query.has('from')).toBe(false);
  });
});
