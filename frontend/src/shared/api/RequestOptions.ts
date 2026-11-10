export default class RequestOptions {
  private headers: [string, string][];
  private method: string | undefined;
  private credentials: 'include' | undefined;
  private body: string | undefined;
  private signal: AbortSignal | undefined;

  constructor() {
    this.headers = [];
  }

  setMethod(method: 'GET' | 'POST' | 'DELETE' | 'PUT' | 'PATCH'): RequestOptions {
    this.method = method;
    return this;
  }

  setCredentials(credentials: 'include'): RequestOptions {
    this.credentials = credentials;
    return this;
  }

  setBody(body: string): RequestOptions {
    this.body = body;
    return this;
  }

  // Aborting cancels the request - and on the Site the query behind it.
  setSignal(signal: AbortSignal | undefined): RequestOptions {
    this.signal = signal;
    return this;
  }

  getBody(): string | undefined {
    return this.body;
  }

  addHeader(headerName: string, headerValue?: string): RequestOptions {
    this.headers.push([headerName, headerValue || '']);
    return this;
  }

  toRequestInit(): RequestInit {
    const headersMatrix: string[][] = [];
    this.headers.forEach(([headerName, headerValue]) => {
      const headerArray: string[] = [];
      headerArray.push(headerName);
      headerArray.push(headerValue);
      headersMatrix.push(headerArray);
    });

    const requestJsonOptions: RequestInit = {
      headers: headersMatrix as [string, string][],
      cache: 'no-cache',
    };

    if (this.method) {
      requestJsonOptions.method = this.method;
    }

    if (this.credentials) {
      requestJsonOptions.credentials = this.credentials;
    }

    if (this.body) {
      requestJsonOptions.body = this.body;
    }

    if (this.signal) {
      requestJsonOptions.signal = this.signal;
    }

    return requestJsonOptions;
  }
}
