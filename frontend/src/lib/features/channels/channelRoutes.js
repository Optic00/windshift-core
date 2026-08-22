export function channelAdminRoute(channel) {
  if (channel.type === 'form') return `/admin/channels/${channel.id}/forms`;
  if (channel.type === 'portal') return `/admin/channels/${channel.id}/portal`;
  return null;
}
