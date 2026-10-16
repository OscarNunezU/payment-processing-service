export const STATUSES = ["PENDING", "PROCESSING", "APPROVED", "DECLINED", "FAILED"] as const;

export type Status = (typeof STATUSES)[number];

export function isStatus(value: string | undefined): value is Status {
  return STATUSES.includes(value as Status);
}

// A final status never changes again, so there is nothing left to refresh.
export function isFinal(status: Status): boolean {
  return status === "APPROVED" || status === "DECLINED" || status === "FAILED";
}
