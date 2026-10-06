"use client";

import { useRouter } from "next/navigation";
import { useEffect } from "react";
import { Loading } from "@/components/ui";

// Old pipeline editor links now open the simple schedule editor.
export default function PipelineEditRedirect() {
  const router = useRouter();
  useEffect(() => {
    router.replace(`/schedules/simple/${window.location.search}`);
  }, [router]);
  return <Loading />;
}
