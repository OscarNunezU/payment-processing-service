// The health probe of Compose: the server is up and answering.
export function GET(): Response {
  return new Response("ok");
}
