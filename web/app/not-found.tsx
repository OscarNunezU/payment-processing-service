import Link from "next/link";

import styles from "./feedback.module.css";

export default function NotFound() {
  return (
    <div className={`card ${styles.message}`}>
      <p className={styles.title}>Esta página no existe</p>
      <p className="muted">
        Vuelve a <Link href="/">los pagos</Link>.
      </p>
    </div>
  );
}
