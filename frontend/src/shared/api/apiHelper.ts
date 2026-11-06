import { accessTokenHelper } from '.';
import RequestOptions from './RequestOptions';

const REPEAT_TRIES_COUNT = 10;
const REPEAT_INTERVAL_MS = 3_000;

// A 4xx is the server's final answer: NonRetryableError skips makeRequest's retry loop,
// while a plain Error (network failure, 5xx) is retried.
class NonRetryableError extends Error {
  readonly status: number;

  constructor(message: string, status: number) {
    super(message);
    this.status = status;
  }
}

/** The HTTP status of a 4xx the server answered with, so callers can tell e.g. a 404 apart. */
export const getHttpStatus = (e: unknown): number | undefined =>
  e instanceof NonRetryableError ? e.status : undefined;

// "Site eu-west is unavailable (HTTP 503)" for a control-plane proxied call.
const unavailableMessage = (url: string, status: number): string => {
  const siteId = url.match(/\/sites\/([^/]+)\//)?.[1];
  const who = siteId ? `Site ${decodeURIComponent(siteId)}` : 'The server';
  return `${who} is unavailable (HTTP ${status}) - it may be restarting or deploying`;
};

const handleOrThrowMessageIfResponseError = async (
  url: string,
  response: Response,
  handleNotAuthorizedError = true,
) => {
  if (handleNotAuthorizedError && response.status === 401) {
    accessTokenHelper?.cleanAccessToken();
    window.location.reload();
  }

  if (response.status === 502 || response.status === 503 || response.status === 504) {
    // Usually a proxy/ingress page (HTML) while a Site is down or deploying;
    // the control plane's own JSON answer says why it couldn't reach one.
    let reason: string | undefined;
    try {
      reason = ((await response.json()) as { error?: string }).error;
    } catch {
      /* not JSON - an ingress page */
    }
    const siteId = url.match(/\/sites\/([^/]+)\//)?.[1];
    throw new Error(
      reason && siteId
        ? `Site ${decodeURIComponent(siteId)}: ${reason}`
        : unavailableMessage(url, response.status),
    );
  }

  if (response.status >= 400 && response.status <= 600) {
    // Read the body once as text, then try JSON: a Response body is one-shot, so a json()
    // then text() fallback lost the real error to "Body has already been consumed".
    let errorMessage: string | undefined;
    let bodyText = '';

    try {
      bodyText = await response.text();
    } catch {
      /* ignore - a body that can't be read at all just yields the status line */
    }

    if (bodyText) {
      try {
        const json = JSON.parse(bodyText) as { message?: string; error?: string };
        errorMessage = json.message || json.error;
      } catch {
        // Never show a page's source (an HTML error page) as the message.
        errorMessage = /^\s*</.test(bodyText) ? undefined : bodyText.slice(0, 300);
      }
    }

    const message =
      errorMessage ||
      (response.status >= 500
        ? unavailableMessage(url, response.status)
        : `${url}: HTTP ${response.status}`);
    // A 4xx won't succeed on retry, so it must not wait out the retry loop.
    if (response.status >= 400 && response.status < 500) {
      throw new NonRetryableError(message, response.status);
    }
    throw new Error(message);
  }
};

const makeRequest = async (
  url: string,
  optionsWrapper: RequestOptions,
  currentTry = 0,
): Promise<Response> => {
  try {
    const response = await fetch(url, optionsWrapper.toRequestInit());
    await handleOrThrowMessageIfResponseError(url, response);
    return response;
  } catch (e) {
    if (e instanceof NonRetryableError || (e instanceof DOMException && e.name === 'AbortError')) {
      throw e;
    }

    if (currentTry < REPEAT_TRIES_COUNT) {
      await new Promise((resolve) => setTimeout(resolve, REPEAT_INTERVAL_MS));
      return makeRequest(url, optionsWrapper, currentTry + 1);
    }

    throw e;
  }
};

export const apiHelper = {
  fetchPostJson: async <T>(
    url: string,
    requestOptions?: RequestOptions,
    isRetryOnError = false,
  ): Promise<T> => {
    const optionsWrapper = (requestOptions ?? new RequestOptions())
      .setMethod('POST')
      .addHeader('Content-Type', 'application/json')
      .addHeader('Accept', 'application/json')
      .addHeader('Authorization', accessTokenHelper.getAccessToken());

    const response = await makeRequest(
      url,
      optionsWrapper,
      isRetryOnError ? 0 : REPEAT_TRIES_COUNT,
    );

    return response.json();
  },

  fetchPostRaw: async (
    url: string,
    requestOptions?: RequestOptions,
    isRetryOnError = false,
  ): Promise<string> => {
    const optionsWrapper = (requestOptions ?? new RequestOptions())
      .setMethod('POST')
      .addHeader('Content-Type', 'application/json')
      .addHeader('Accept', 'application/json')
      .addHeader('Authorization', accessTokenHelper.getAccessToken());

    const response = await makeRequest(
      url,
      optionsWrapper,
      isRetryOnError ? 0 : REPEAT_TRIES_COUNT,
    );

    return response.text();
  },

  fetchPostBlob: async (
    url: string,
    requestOptions?: RequestOptions,
    isRetryOnError = false,
  ): Promise<Blob> => {
    const optionsWrapper = (requestOptions ?? new RequestOptions())
      .setMethod('POST')
      .addHeader('Content-Type', 'application/json')
      .addHeader('Authorization', accessTokenHelper.getAccessToken());

    const response = await makeRequest(
      url,
      optionsWrapper,
      isRetryOnError ? 0 : REPEAT_TRIES_COUNT,
    );

    return response.blob();
  },

  fetchGetJson: async <T>(
    url: string,
    requestOptions?: RequestOptions,
    isRetryOnError = false,
  ): Promise<T> => {
    const optionsWrapper = (requestOptions ?? new RequestOptions())
      .addHeader('Content-Type', 'application/json')
      .addHeader('Accept', 'application/json')
      .addHeader('Authorization', accessTokenHelper.getAccessToken());

    const response = await makeRequest(
      url,
      optionsWrapper,
      isRetryOnError ? 0 : REPEAT_TRIES_COUNT,
    );

    return response.json();
  },

  fetchGetRaw: async (
    url: string,
    requestOptions?: RequestOptions,
    isRetryOnError = false,
  ): Promise<string> => {
    const optionsWrapper = (requestOptions ?? new RequestOptions())
      .addHeader('Content-Type', 'application/json')
      .addHeader('Accept', 'application/json')
      .addHeader('Authorization', accessTokenHelper.getAccessToken());

    const response = await makeRequest(
      url,
      optionsWrapper,
      isRetryOnError ? 0 : REPEAT_TRIES_COUNT,
    );

    return response.text();
  },

  fetchGetBlob: async (
    url: string,
    requestOptions?: RequestOptions,
    isRetryOnError = false,
  ): Promise<Blob> => {
    const optionsWrapper = (requestOptions ?? new RequestOptions()).addHeader(
      'Authorization',
      accessTokenHelper.getAccessToken(),
    );

    const response = await makeRequest(
      url,
      optionsWrapper,
      isRetryOnError ? 0 : REPEAT_TRIES_COUNT,
    );

    return response.blob();
  },

  fetchPutJson: async <T>(
    url: string,
    requestOptions?: RequestOptions,
    isRetryOnError = false,
  ): Promise<T> => {
    const optionsWrapper = (requestOptions ?? new RequestOptions())
      .setMethod('PUT')
      .addHeader('Content-Type', 'application/json')
      .addHeader('Accept', 'application/json')
      .addHeader('Authorization', accessTokenHelper.getAccessToken());

    const response = await makeRequest(
      url,
      optionsWrapper,
      isRetryOnError ? 0 : REPEAT_TRIES_COUNT,
    );

    return response.json();
  },

  fetchDeleteJson: async <T>(
    url: string,
    requestOptions?: RequestOptions,
    isRetryOnError = false,
  ): Promise<T> => {
    const optionsWrapper = (requestOptions ?? new RequestOptions())
      .setMethod('DELETE')
      .addHeader('Accept', 'application/json')
      .addHeader('Authorization', accessTokenHelper.getAccessToken());

    const response = await makeRequest(
      url,
      optionsWrapper,
      isRetryOnError ? 0 : REPEAT_TRIES_COUNT,
    );

    return response.json();
  },

  fetchDeleteRaw: async (
    url: string,
    requestOptions?: RequestOptions,
    isRetryOnError = false,
  ): Promise<string> => {
    const optionsWrapper = (requestOptions ?? new RequestOptions())
      .setMethod('DELETE')
      .addHeader('Accept', 'application/json')
      .addHeader('Authorization', accessTokenHelper.getAccessToken());

    const response = await makeRequest(
      url,
      optionsWrapper,
      isRetryOnError ? 0 : REPEAT_TRIES_COUNT,
    );

    return response.text();
  },
};
