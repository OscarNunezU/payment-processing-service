// What a refused POST /v1/payments says about the payment. A 4xx is refused
// before anything is stored, and acquirer_unavailable by the gate inside T1,
// which rolls the insert back (RFC 0001, section 5.1): no payment exists.
// Any other 5xx, such as the database failing during the insert or its
// commit, leaves it unknown: the payment may exist, and sending the request
// again with the same key is how to find out.
export function refusal(httpStatus: number, code: string): "not_created" | "uncertain" {
  return httpStatus < 500 || code === "acquirer_unavailable" ? "not_created" : "uncertain";
}
