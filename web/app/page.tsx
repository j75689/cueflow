"use client";

import { useRouter } from "next/navigation";
import { useEffect } from "react";
import { Loading } from "@/components/ui";

// The review queue lives under Deliverables; the root only forwards there.
export default function HomePage() {
  const router = useRouter();
  useEffect(() => {
    router.replace("/deliverables/");
  }, [router]);
  return <Loading />;
}
