const CONNECTION_TEST_FALLBACK = 'Connection test failed';

function errorMessageFromPayload(payload) {
  if (!payload) return '';

  if (Array.isArray(payload)) {
    for (const entry of payload) {
      const message = errorMessageFromPayload(entry);
      if (message) return message;
    }
    return '';
  }

  if (typeof payload !== 'object') return '';

  if (payload.error && typeof payload.error === 'object') {
    const message = errorMessageFromPayload(payload.error);
    if (message) return message;
  }
  if (typeof payload.message === 'string' && payload.message.trim()) {
    return payload.message.trim();
  }
  if (typeof payload.error === 'string' && payload.error.trim()) {
    return payload.error.trim();
  }

  return '';
}

function parseEmbeddedProviderError(message) {
  const jsonStarts = [message.indexOf('{'), message.indexOf('[')]
    .filter((index) => index >= 0)
    .sort((a, b) => a - b);

  for (const start of jsonStarts) {
    try {
      const providerMessage = errorMessageFromPayload(JSON.parse(message.slice(start)));
      if (providerMessage) return providerMessage;
    } catch {
      // The API error may contain ordinary prose with braces. Try the next shape.
    }
  }

  const providerResponse = message.match(/LLM API error:\s*(?:status\s*)?\d+\s*[-:]\s*([\s\S]+)$/i);
  return providerResponse?.[1]?.trim() || '';
}

/**
 * Extract the provider/model message from a failed LLM connection test.
 * OpenAI-compatible gateways return their error envelope inside Windshift's
 * own API error string, so the generic API client cannot unwrap it directly.
 */
export function llmConnectionTestErrorMessage(error) {
  const message =
    (typeof error?.body?.error === 'string' && error.body.error) ||
    (typeof error?.body?.message === 'string' && error.body.message) ||
    (typeof error?.message === 'string' && error.message) ||
    (typeof error === 'string' && error) ||
    '';

  if (!message.trim()) return CONNECTION_TEST_FALLBACK;
  return parseEmbeddedProviderError(message) || message.trim();
}
