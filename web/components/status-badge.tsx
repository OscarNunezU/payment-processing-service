import { isFinal, type Status } from "@/lib/statuses";

import styles from "./status-badge.module.css";

// The dot pulses while the payment is still in flight.
export function StatusBadge({ status }: { status: Status }) {
  const className = [styles.badge, styles[status], isFinal(status) ? "" : styles.live].join(" ");
  return <span className={className}>{status}</span>;
}
