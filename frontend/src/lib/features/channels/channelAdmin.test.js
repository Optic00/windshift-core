import { describe, expect, test } from 'vitest';
import { parseChannelConfig } from './channelAdmin.js';

describe('parseChannelConfig', () => {
  test('uses an empty object for absent or blank config', () => {
    expect(parseChannelConfig(null)).toEqual({});
    expect(parseChannelConfig(undefined)).toEqual({});
    expect(parseChannelConfig('')).toEqual({});
    expect(parseChannelConfig('   ')).toEqual({});
  });

  test('accepts object config in either wire representation', () => {
    const config = { portal_slug: 'support' };
    expect(parseChannelConfig(config)).toBe(config);
    expect(parseChannelConfig(JSON.stringify(config))).toEqual(config);
  });

  test.each([false, 0, [], '[1]', 'null', 'false'])('rejects non-object config: %j', (config) => {
    expect(() => parseChannelConfig(config)).toThrow('Channel configuration');
  });

  test('rejects malformed JSON', () => {
    expect(() => parseChannelConfig('{')).toThrow('Channel configuration is invalid JSON');
  });
});
