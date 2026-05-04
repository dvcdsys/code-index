// Lightweight fetch wrapper used by every TanStack Query call.
//
// Two contracts the rest of the app relies on:
//
//   1. Cookie-based auth: requests are sent with `credentials: 'same-origin'`
//      so the HttpOnly `cix_session` cookie set by /api/v1/auth/login flows
//      automatically. No tokens in localStorage; no Authorization header.
//
//   2. Error normalisation: any non-2xx response is translated into an
//      `ApiError` whose `.detail` mirrors the FastAPI-style {"detail": "..."}
//      payload our handlers always emit. Callers can `instanceof ApiError`
//      and check `.status === 401` to drive auth redirects.

const API_PREFIX = '/api/v1';

export class ApiError extends Error {
  status: number;
  detail: string;
  constructor(status: number, detail: string) {
    super(`HTTP ${status}: ${detail}`);
    this.name = 'ApiError';
    this.status = status;
    this.detail = detail;
  }
}

export interface RequestOptions {
  method?: 'GET' | 'POST' | 'PATCH' | 'PUT' | 'DELETE';
  /** Plain object — serialized as JSON. */
  body?: unknown;
  /** Extra query-string params; values are stringified. */
  query?: Record<string, string | number | boolean | undefined | null>;
  /** Cancel signal from React Query. */
  signal?: AbortSignal;
}

function buildUrl(path: string, query?: RequestOptions['query']): string {
  // Path always starts with `/auth/...` etc. The /api/v1 prefix is added here
  // so individual call sites stay short and won't drift from the OpenAPI spec.
  const base = path.startsWith('/api/') ? path : `${API_PREFIX}${path}`;
  if (!query) return base;
  const params = new URLSearchParams();
  for (const [k, v] of Object.entries(query)) {
    if (v === undefined || v === null) continue;
    params.set(k, String(v));
  }
  const qs = params.toString();
  return qs ? `${base}?${qs}` : base;
}

async function readDetail(res: Response): Promise<string> {
  try {
    const data = (await res.clone().json()) as { detail?: unknown };
    if (data && typeof data.detail === 'string') return data.detail;
  } catch {
    // fall through — non-JSON body
  }
  try {
    const txt = await res.text();
    return txt || res.statusText || `HTTP ${res.status}`;
  } catch {
    return res.statusText || `HTTP ${res.status}`;
  }
}

export async function request<T = unknown>(
  path: string,
  opts: RequestOptions = {}
): Promise<T> {
  const { method = 'GET', body, query, signal } = opts;

  const init: RequestInit = {
    method,
    credentials: 'same-origin',
    headers: { Accept: 'application/json' },
    signal,
  };

  if (body !== undefined) {
    init.headers = {
      ...(init.headers as Record<string, string>),
      'Content-Type': 'application/json',
    };
    init.body = JSON.stringify(body);
  }

  const res = await fetch(buildUrl(path, query), init);

  if (!res.ok) {
    throw new ApiError(res.status, await readDetail(res));
  }

  // 204 No Content + empty body for DELETEs.
  if (res.status === 204) return undefined as T;
  const ctype = res.headers.get('content-type') || '';
  if (!ctype.includes('application/json')) return undefined as T;
  return (await res.json()) as T;
}

export const api = {
  get: <T>(path: string, opts?: Omit<RequestOptions, 'method' | 'body'>) =>
    request<T>(path, { ...opts, method: 'GET' }),
  post: <T>(path: string, body?: unknown, opts?: Omit<RequestOptions, 'method' | 'body'>) =>
    request<T>(path, { ...opts, method: 'POST', body }),
  patch: <T>(path: string, body?: unknown, opts?: Omit<RequestOptions, 'method' | 'body'>) =>
    request<T>(path, { ...opts, method: 'PATCH', body }),
  put: <T>(path: string, body?: unknown, opts?: Omit<RequestOptions, 'method' | 'body'>) =>
    request<T>(path, { ...opts, method: 'PUT', body }),
  delete: <T>(path: string, opts?: Omit<RequestOptions, 'method' | 'body'>) =>
    request<T>(path, { ...opts, method: 'DELETE' }),
};
