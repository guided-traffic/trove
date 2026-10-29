import type { components, paths } from './generated/schema';

/** Schemas of the OpenAPI document (api/openapi.yaml), by name. */
export type Schema<N extends keyof components['schemas']> = components['schemas'][N];

/** Paths that offer a GET with a JSON 200 answer. */
export type GetPath = {
  [P in keyof paths]: paths[P] extends {
    get: { responses: { 200: { content: { 'application/json': unknown } } } };
  }
    ? P
    : never;
}[keyof paths];

/** The JSON body of the 200 answer of GET `P`. */
export type GetBody<P extends GetPath> = paths[P] extends {
  get: { responses: { 200: { content: { 'application/json': infer B } } } };
}
  ? B
  : never;

/** A JSON answer with the metadata the UI needs next to the body. */
export interface ApiResponse<T> {
  readonly body: T;
  readonly status: number;
  /** The strong `ETag` of the representation, quotes included (R-API-012). */
  readonly etag: string | null;
  /** `Location` of a created resource or a started job. */
  readonly location: string | null;
}
