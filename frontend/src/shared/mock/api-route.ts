import {
  OPENAPI_BASE_PATH,
  OPENAPI_ROUTES,
  type OpenApiRouteSpec,
} from "@/shared/mock/openapi-routes.generated";

function escapeRegex(text: string): string {
  return text.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}

function normalizeRouteShape(pathTemplate: string): string {
  return pathTemplate.replace(/:[A-Za-z0-9_]+/g, ":*");
}

const KNOWN_OPENAPI_SHAPES: ReadonlySet<string> = new Set(
  OPENAPI_ROUTES.map((route) => normalizeRouteShape(route.path)),
);

const BASE_PATH_REGEX = escapeRegex(OPENAPI_BASE_PATH);

/**
 * Builds an exact MSW URL RegExp anchored on the OpenAPI base prefix (`/api/v4`).
 * Rejects wildcard `*` patterns, missing leading `/`, accidental `/api/` prefixes,
 * or paths absent from `api/openapi/yearning-v4.yaml`.
 */
export function apiRoute(pathTemplate: string): RegExp {
  if (!pathTemplate.startsWith("/")) {
    throw new Error(`apiRoute("${pathTemplate}"): path must start with "/"`);
  }
  if (pathTemplate.includes("*") || pathTemplate.includes("?")) {
    throw new Error(`apiRoute("${pathTemplate}"): wildcards and query strings are forbidden`);
  }
  if (pathTemplate.startsWith("/api/")) {
    throw new Error(
      `apiRoute("${pathTemplate}"): pass relative OpenAPI path without "${OPENAPI_BASE_PATH}" prefix`,
    );
  }
  if (!KNOWN_OPENAPI_SHAPES.has(normalizeRouteShape(pathTemplate))) {
    throw new Error(`apiRoute("${pathTemplate}"): path is not declared in OpenAPI contract`);
  }

  const segments = pathTemplate
    .split("/")
    .filter((segment) => segment.length > 0)
    .map((segment) => {
      if (segment.startsWith(":")) {
        const paramName = segment.slice(1);
        if (!/^[A-Za-z0-9_]+$/.test(paramName)) {
          throw new Error(`apiRoute("${pathTemplate}"): invalid param segment "${segment}"`);
        }
        return `(?<${paramName}>[^/]+)`;
      }
      return escapeRegex(segment);
    });

  const suffix = segments.length === 0 ? "" : `\\/${segments.join("\\/")}`;
  return new RegExp(`^https?:\\/\\/[^/]+${BASE_PATH_REGEX}${suffix}$`);
}

interface CompiledRoute {
  readonly spec: OpenApiRouteSpec;
  readonly segments: readonly string[];
  readonly specificity: number;
}

const COMPILED_ROUTES: readonly CompiledRoute[] = OPENAPI_ROUTES.map((spec) => {
  const segments = spec.path.split("/").filter((segment) => segment.length > 0);
  let staticCount = 0;
  let paramCount = 0;
  for (const segment of segments) {
    if (segment.startsWith(":")) {
      paramCount += 1;
    } else {
      staticCount += 1;
    }
  }
  return {
    spec,
    segments,
    specificity: staticCount * 100 - paramCount,
  };
}).sort((a, b) => b.specificity - a.specificity);

/**
 * Resolves an HTTP method + full request URL (requiring `/api/v4` prefix) to
 * its most specific OpenAPI route spec for runtime Zod response checks.
 */
export function matchOpenApiRoute(method: string, urlString: string): OpenApiRouteSpec | null {
  let pathname: string;
  try {
    pathname = new URL(urlString, "http://localhost").pathname;
  } catch {
    return null;
  }

  if (
    pathname !== OPENAPI_BASE_PATH &&
    !pathname.startsWith(`${OPENAPI_BASE_PATH}/`)
  ) {
    return null;
  }

  const relativePath = pathname.slice(OPENAPI_BASE_PATH.length);
  const actualSegments = relativePath.split("/").filter((segment) => segment.length > 0);
  const upperMethod = method.toUpperCase();

  for (const candidate of COMPILED_ROUTES) {
    if (candidate.spec.method !== upperMethod) {
      continue;
    }
    if (candidate.segments.length !== actualSegments.length) {
      continue;
    }
    const matched = candidate.segments.every(
      (expected, index) => expected.startsWith(":") || expected === actualSegments[index],
    );
    if (matched) {
      return candidate.spec;
    }
  }
  return null;
}
