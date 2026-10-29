import { receive } from "@/lib/webhooks";

// The merchant's webhook endpoint (RFC 0001, section 12). It reads the body as
// text, since the signature covers the bytes as they were sent.
export async function POST(request: Request): Promise<Response> {
  const status = receive(
    request.headers.get("Webhook-Signature") ?? "",
    request.headers.get("Webhook-Id") ?? "",
    await request.text(),
    new Date(),
  );
  return new Response(null, { status });
}
