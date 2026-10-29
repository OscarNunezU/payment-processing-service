"use server";

import { revalidatePath } from "next/cache";

import { setFailing } from "@/lib/webhooks";

export async function toggleFailing(form: FormData): Promise<void> {
  setFailing(form.get("failing") === "on");
  revalidatePath("/webhooks");
}
