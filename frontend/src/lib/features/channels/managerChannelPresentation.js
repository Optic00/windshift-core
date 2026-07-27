import { parseChannelConfig } from './channelAdmin.js';

export function managerChannelPurpose(channel, workspaceName = '') {
  let config = {};
  try {
    config = parseChannelConfig(channel?.config);
  } catch {
    // A malformed legacy config must not make the manager list unusable.
  }

  if (channel?.type === 'portal' && config.portal_slug) {
    return {
      key: 'channels.manager.publishedAt',
      params: { path: `/portal/${config.portal_slug}` },
    };
  }
  if (channel?.type === 'form' && config.form_slug) {
    return {
      key: 'channels.manager.publishedAt',
      params: { path: `/forms/${config.form_slug}` },
    };
  }
  if (channel?.type === 'email' || channel?.type === 'imap') {
    return {
      key: 'channels.manager.deliversTo',
      params: { workspace: workspaceName || String(config.email_workspace_id || '') },
    };
  }
  if (channel?.type === 'smtp') {
    return { key: 'channels.manager.outboundNotifications', params: {} };
  }
  if (channel?.type === 'webhook') {
    return { key: 'channels.manager.outboundEvents', params: {} };
  }
  return { key: 'channels.manager.operational', params: {} };
}

export function managerChannelStatusColor(status) {
  return ['enabled', 'active', 'configured'].includes(status) ? 'green' : 'gray';
}
