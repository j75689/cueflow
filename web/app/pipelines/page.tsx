"use client";

import { useRouter } from "next/navigation";
import { useEffect } from "react";
import { Loading } from "@/components/ui";

// Pipelines are now "simple schedules" on the Schedules page; keep old links working.
export default function PipelinesRedirect() {
  const router = useRouter();
  useEffect(() => router.replace("/schedules/"), [router]);
  return <Loading />;
}
