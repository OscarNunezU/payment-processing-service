"use server";

import { revalidatePath } from "next/cache";

import { putFaults } from "@/lib/acquirer";

export type State = { error?: string };

// The simulator validates the faults itself, and its message comes back as
// the form's error.
export async function saveFaults(_previous: State, form: FormData): Promise<State> {
  const error = await putFaults({
    latency_ms: Number(form.get("latency_ms") ?? 0),
    error_rate: Number(form.get("error_rate") ?? 0),
    timeout_rate: Number(form.get("timeout_rate") ?? 0),
    down: form.get("down") === "on",
  }).catch(() => "El adquirente no responde en su puerto de administración.");
  revalidatePath("/acquirer");
  return error ? { error } : {};
}
