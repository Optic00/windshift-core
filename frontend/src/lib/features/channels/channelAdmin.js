import { api } from '../../api.js';

export function parseChannelConfig(config) {
  if (!config) return {};
  if (typeof config === 'string') {
    if (config.trim() === '') return {};
    try {
      return JSON.parse(config);
    } catch {
      return {};
    }
  }
  return config || {};
}

export function channelBasicFormData(channel) {
  return {
    name: channel?.name || '',
    description: channel?.description || '',
    category_id: channel?.category_id || null,
  };
}

export async function saveChannelSettings({ channel, channelFormData, configRef, enabled }) {
  await api.channels.update(channel.id, {
    id: channel.id,
    type: channel.type,
    direction: channel.direction,
    is_default: channel.is_default,
    name: channelFormData.name,
    description: channelFormData.description,
    category_id: channelFormData.category_id,
  });

  if (configRef) {
    await api.channels.updateConfig(channel.id, {
      ...parseChannelConfig(channel.config),
      ...configRef.getConfig(),
    });
  }

  const currentlyEnabled = channel.status === 'enabled';
  if (enabled !== currentlyEnabled) {
    await api.channels.toggle(channel.id);
  }
}
