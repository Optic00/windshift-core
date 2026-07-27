export function safeLoginReturnPath(search) {
  const value = new URLSearchParams(search).get('return_to');
  if (
    !value ||
    value !== value.trim() ||
    !value.startsWith('/') ||
    value.startsWith('//') ||
    value.includes('\\') ||
    value.startsWith('/login')
  ) {
    return '';
  }
  return value;
}
