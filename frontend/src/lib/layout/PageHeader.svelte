<script>
  import BaseHeader from './BaseHeader.svelte';

  let {
    icon = null,
    title = '',
    subtitle = '',
    description = '',
    count = null,
    textStyle = '',
    subtitleStyle = '',
    children = null,
    actions = null
  } = $props();
</script>

<BaseHeader
  {icon}
  {title}
  {subtitle}
  {description}
  {count}
  {children}
  {actions}
  {textStyle}
  {subtitleStyle}
  iconStyle={subtitleStyle}
/>