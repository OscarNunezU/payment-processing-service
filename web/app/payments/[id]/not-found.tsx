import Link from "next/link";

import styles from "@/app/feedback.module.css";

export default function PaymentNotFound() {
  return (
    <div className={`card ${styles.message}`}>
      <p className={styles.title}>Ese pago no existe</p>
      <p className="muted">
        Revisa el id o vuelve a <Link href="/">los pagos</Link>.
      </p>
    </div>
  );
}
