import type { AssertionKind } from '../../../entity/synthetics';

export const METHODS = ['GET', 'HEAD', 'POST', 'PUT', 'PATCH', 'DELETE', 'OPTIONS'];

export const HTTP_ASSERTIONS: AssertionKind[] = [
  'status',
  'response_time_ms',
  'header',
  'json',
  'body',
];
