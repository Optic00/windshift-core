import { api } from '../../api.js';

export function parseChannelConfig(config) {
  if (config == null) return {};
  if (typeof config === 'string') {
    if (config.trim() === '') return {};
    try {
      const parsed = JSON.parse(config);
      if (!parsed || Array.isArray(parsed) || typeof parsed !== 'object') {
        throw new Error('Channel configuration must be a JSON object');
      }
      return parsed;
    } catch (error) {
      throw new Error('Channel configuration is invalid JSON', { cause: error });
    }
  }
  if (Array.isArray(config) || typeof config !== 'object') {
    throw new Error('Channel configuration must be a JSON object');
  }
  return config;
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
    await api.channels.updateConfig(channel.id, configRef.getConfig());
  }

  const currentlyEnabled = channel.status === 'enabled';
  if (typeof enabled === 'boolean' && enabled !== currentlyEnabled) {
    await api.channels.toggle(channel.id);
  }
}

export async function prepareFormChannelForWorkspace({ channel, workspaceIds, workspaceId }) {
  const currentWorkspaceIds = workspaceIds || [];
  const nextWorkspaceIds = currentWorkspaceIds.includes(workspaceId)
    ? currentWorkspaceIds
    : [...currentWorkspaceIds, workspaceId];

  if (nextWorkspaceIds !== currentWorkspaceIds) {
    await api.channels.updateConfig(channel.id, {
      form_workspace_ids: nextWorkspaceIds,
    });
  }

  let status = channel.status;
  if (status !== 'enabled') {
    const updated = await api.channels.toggle(channel.id);
    status = updated?.status || 'enabled';
  }

  return { workspaceIds: nextWorkspaceIds, status };
}
