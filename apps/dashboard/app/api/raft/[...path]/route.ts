import { NextRequest } from "next/server";

const methods = ["GET", "POST", "PATCH", "DELETE"] as const;

type Context = {
  params: Promise<{ path: string[] }>;
};

async function proxy(request: NextRequest, context: Context): Promise<Response> {
  const endpoints = (process.env.QFLAG_NODES ?? "")
    .split(",")
    .map((endpoint) => endpoint.trim().replace(/\/$/, ""))
    .filter(Boolean);
  const token = process.env.API_TOKEN;

  if (endpoints.length === 0 || !token) {
    return Response.json({ error: "dashboard backend is not configured" }, { status: 503 });
  }

  const { path } = await context.params;
  const suffix = `/${path.map(encodeURIComponent).join("/")}${request.nextUrl.search}`;
  const body = request.method === "GET" ? undefined : await request.arrayBuffer();
  let lastError: Response | undefined;
  let networkFailure = false;

  for (const endpoint of endpoints) {
    try {
      const response = await fetch(endpoint + suffix, {
        method: request.method,
        headers: {
          Authorization: `Bearer ${token}`,
          "Content-Type": request.headers.get("content-type") ?? "application/json",
          "X-Actor": request.headers.get("x-actor") ?? "dashboard",
        },
        body,
        cache: "no-store",
      });
      if (response.ok) {
        const headers = new Headers();
        const contentType = response.headers.get("content-type");
        if (contentType) headers.set("content-type", contentType);
        return new Response(response.body, { status: response.status, headers });
      }
      lastError = response;
    } catch {
      networkFailure = true;
    }
  }

  if (lastError) {
    return new Response(lastError.body, { status: lastError.status, headers: lastError.headers });
  }
  return Response.json(
    { error: networkFailure ? "QFlag API unavailable" : "No QFlag endpoint responded" },
    { status: 503 },
  );
}

export const dynamic = "force-dynamic";

export const GET = proxy;
export const POST = proxy;
export const PATCH = proxy;
export const DELETE = proxy;
